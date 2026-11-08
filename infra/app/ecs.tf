resource "aws_cloudwatch_log_group" "app" {
  name              = "/ecs/${local.name}"
  retention_in_days = var.log_retention_days
}

resource "aws_ecs_cluster" "this" {
  name = local.name
  setting {
    name  = "containerInsights"
    value = "disabled" # extra CloudWatch cost; the ALB/RDS metrics and dashboard are enough here
  }
}

# ----------------------------------------------------------------------- IAM
data "aws_iam_policy_document" "ecs_tasks_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [data.aws_caller_identity.current.account_id]
    }
  }
}

# Every IAM role here carries the aws-foundation permissions boundary (variables.tf).
locals {
  permissions_boundary = var.permissions_boundary_name == "" ? null : "arn:${data.aws_partition.current.partition}:iam::${data.aws_caller_identity.current.account_id}:policy/${var.permissions_boundary_name}"
}

# Used by ECS itself: pull images, write logs, read the DB password.
resource "aws_iam_role" "execution" {
  name                 = "${local.name}-ecs-execution"
  assume_role_policy   = data.aws_iam_policy_document.ecs_tasks_assume.json
  permissions_boundary = local.permissions_boundary
}

resource "aws_iam_role_policy_attachment" "execution" {
  role       = aws_iam_role.execution.name
  policy_arn = "arn:${data.aws_partition.current.partition}:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

data "aws_iam_policy_document" "read_db_secret" {
  statement {
    actions   = ["secretsmanager:GetSecretValue"]
    resources = [aws_db_instance.this.master_user_secret[0].secret_arn]
  }
}

resource "aws_iam_role_policy" "read_db_secret" {
  name   = "${local.name}-read-db-secret"
  role   = aws_iam_role.execution.id
  policy = data.aws_iam_policy_document.read_db_secret.json
}

# Used by the application code. It calls no AWS APIs, so it gets no permissions.
resource "aws_iam_role" "task" {
  name                 = "${local.name}-ecs-task"
  assume_role_policy   = data.aws_iam_policy_document.ecs_tasks_assume.json
  permissions_boundary = local.permissions_boundary
}

# ------------------------------------------------------------ task definition
locals {
  log_options = {
    awslogs-group         = aws_cloudwatch_log_group.app.name
    awslogs-region        = var.aws_region
    awslogs-stream-prefix = "app"
  }
  db_env = [
    { name = "DB_HOST", value = aws_db_instance.this.address },
    { name = "DB_PORT", value = tostring(aws_db_instance.this.port) },
    { name = "DB_NAME", value = aws_db_instance.this.db_name },
    { name = "DB_USER", value = aws_db_instance.this.username },
    { name = "DB_SSLMODE", value = "require" },
    { name = "REDIS_ADDR", value = "localhost:6379" },
  ]
  db_secret = [
    { name = "DB_PASSWORD", valueFrom = "${aws_db_instance.this.master_user_secret[0].secret_arn}:password::" },
  ]
}

# One task = API + click worker + a Redis sidecar (they share localhost).
# scripts/deploy.sh registers new revisions of this family for each release.
resource "aws_ecs_task_definition" "app" {
  family                   = local.name
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.task_cpu
  memory                   = var.task_memory
  execution_role_arn       = aws_iam_role.execution.arn
  task_role_arn            = aws_iam_role.task.arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "ARM64" # Graviton: ~20% cheaper than x86 on Fargate
  }

  volume {
    name = "worker-tmp"
  }

  container_definitions = jsonencode([
    {
      name                   = "redis"
      image                  = var.redis_image
      essential              = true
      command                = ["redis-server", "--save", "", "--appendonly", "no", "--maxmemory", "48mb", "--maxmemory-policy", "noeviction"]
      memoryReservation      = 64
      readonlyRootFilesystem = false
      healthCheck = {
        command     = ["CMD", "redis-cli", "ping"]
        interval    = 10
        timeout     = 3
        retries     = 3
        startPeriod = 5
      }
      logConfiguration = { logDriver = "awslogs", options = local.log_options }
    },
    {
      name                   = "api"
      image                  = var.api_image
      essential              = true
      memoryReservation      = 128
      readonlyRootFilesystem = true
      portMappings           = [{ containerPort = 8080, protocol = "tcp" }]
      environment            = concat(local.db_env, [{ name = "PORT", value = "8080" }, { name = "CHAOS_FAIL_HEALTH", value = "0" }])
      secrets                = local.db_secret
      dependsOn              = [{ containerName = "redis", condition = "HEALTHY" }]
      logConfiguration       = { logDriver = "awslogs", options = local.log_options }
    },
    {
      name                   = "worker"
      image                  = var.worker_image
      essential              = true
      memoryReservation      = 96
      readonlyRootFilesystem = true
      environment            = concat(local.db_env, [{ name = "HEARTBEAT_FILE", value = "/tmp/worker-heartbeat" }])
      secrets                = local.db_secret
      mountPoints            = [{ sourceVolume = "worker-tmp", containerPath = "/tmp", readOnly = false }]
      dependsOn              = [{ containerName = "redis", condition = "HEALTHY" }]
      healthCheck = {
        command     = ["CMD", "python", "healthcheck.py"]
        interval    = 30
        timeout     = 5
        retries     = 3
        startPeriod = 60
      }
      logConfiguration = { logDriver = "awslogs", options = local.log_options }
    },
  ])
}

resource "aws_ecs_service" "app" {
  name                              = local.name
  cluster                           = aws_ecs_cluster.this.id
  task_definition                   = aws_ecs_task_definition.app.arn
  desired_count                     = var.desired_count
  launch_type                       = "FARGATE"
  platform_version                  = "LATEST"
  health_check_grace_period_seconds = 30
  enable_ecs_managed_tags           = true
  propagate_tags                    = "SERVICE"
  wait_for_steady_state             = true

  deployment_minimum_healthy_percent = 100
  deployment_maximum_percent         = 200

  # A release whose tasks keep failing health checks is rolled back to the
  # previous task definition automatically.
  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  network_configuration {
    subnets          = aws_subnet.public[*].id
    security_groups  = [aws_security_group.tasks.id]
    assign_public_ip = true # no NAT gateway; the security group still blocks all direct access
  }

  load_balancer {
    target_group_arn = aws_lb_target_group.api.arn
    container_name   = "api"
    container_port   = 8080
  }

  # Releases are rolled out by scripts/deploy.sh (new task definition revision +
  # update-service), not by Terraform.
  lifecycle {
    ignore_changes = [task_definition]
  }

  depends_on = [aws_lb_listener.http, aws_iam_role_policy.read_db_secret]
}
