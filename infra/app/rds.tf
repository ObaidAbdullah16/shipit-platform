resource "aws_db_subnet_group" "this" {
  name       = "${local.name}-db"
  subnet_ids = aws_subnet.private[*].id
}

# PostgreSQL in private subnets. The master password is generated and stored by
# RDS in Secrets Manager (manage_master_user_password), so it never appears in
# Terraform state, code or logs; ECS injects it into the containers at start.
resource "aws_db_instance" "this" {
  identifier     = "${local.name}-db"
  engine         = "postgres"
  engine_version = "16"
  instance_class = var.db_instance_class

  allocated_storage = 20
  storage_type      = "gp3"
  storage_encrypted = true

  db_name                     = "shipit"
  username                    = "shipit"
  manage_master_user_password = true

  db_subnet_group_name   = aws_db_subnet_group.this.name
  vpc_security_group_ids = [aws_security_group.db.id]
  publicly_accessible    = false
  multi_az               = false

  iam_database_authentication_enabled = true
  auto_minor_version_upgrade          = true
  backup_retention_period             = 1
  copy_tags_to_snapshot               = true
  apply_immediately                   = true
  deletion_protection                 = false # `down` must be able to delete it
  skip_final_snapshot                 = true  # demo data only
}
