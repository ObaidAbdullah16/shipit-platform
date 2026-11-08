#!/usr/bin/env bash
# shellcheck disable=SC2034  # variables defined here are used by the scripts that source it
# Shared helpers for this repo's up/down/status scripts. Sourced, not executed.
# Settings come from the environment, or from ../_setup/config.env when this repo
# sits inside the Projects_Demos folder.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if [[ -f "$REPO_ROOT/../_setup/config.env" ]]; then
  # shellcheck disable=SC1091
  source "$REPO_ROOT/../_setup/config.env"
fi
: "${AWS_REGION:=ap-southeast-2}"
: "${AUTO_APPROVE:=1}"          # set AUTO_APPROVE=0 to review the plan before applying
export AWS_REGION AWS_DEFAULT_REGION="$AWS_REGION"
[[ -n "${AWS_PROFILE:-}" ]] && export AWS_PROFILE
# Optional HTTPS on a sub-domain of your portfolio's Route 53 zone (_setup/config.env: SHIPIT_DOMAIN).
# On GitHub the Control workflow passes TF_VAR_domain_name / TF_VAR_hosted_zone_id from repository variables.
if [[ -z "${TF_VAR_domain_name+x}" && -n "${SHIPIT_DOMAIN:-}" && -n "${PORTFOLIO_ZONE_ID:-}" ]]; then
  export TF_VAR_domain_name="$SHIPIT_DOMAIN" TF_VAR_hosted_zone_id="$PORTFOLIO_ZONE_ID"
fi

log()  { printf '\033[1;34m[%s]\033[0m %s\n' "$(date +%H:%M:%S)" "$*"; }
warn() { printf '\033[1;33m[warn]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[error]\033[0m %s\n' "$*" >&2; exit 1; }

need() {
  for c in "$@"; do
    command -v "$c" >/dev/null 2>&1 || die "'$c' is not installed (see _setup/install-tools-wsl.sh)"
  done
}

aws_account_id() { aws sts get-caller-identity --query Account --output text; }

state_bucket() { echo "${TF_STATE_BUCKET:-tfstate-$(aws_account_id)-${AWS_REGION}}"; }

# tf_init <terraform dir> <state key>
tf_init() {
  local dir="$1" key="$2" bucket
  bucket="$(state_bucket)"
  aws s3api head-bucket --bucket "$bucket" >/dev/null 2>&1 \
    || die "State bucket $bucket not found: the AWS foundation is not set up (or you are logged in to another account). Start it first: bash _setup/projects.sh up foundation"
  terraform -chdir="$dir" init -input=false -reconfigure \
    -backend-config="bucket=${bucket}" \
    -backend-config="key=${key}" \
    -backend-config="region=${AWS_REGION}" \
    -backend-config="use_lockfile=true" \
    -backend-config="encrypt=true" >/dev/null
}

tf_apply() { # tf_apply <dir> [extra args...]
  local dir="$1"; shift
  if [[ "$AUTO_APPROVE" == "1" ]]; then
    terraform -chdir="$dir" apply -input=false -auto-approve -var "aws_region=${AWS_REGION}" "$@"
  else
    terraform -chdir="$dir" apply -input=false -var "aws_region=${AWS_REGION}" "$@"
  fi
}

tf_destroy() { # tf_destroy <dir> [extra args...]
  local dir="$1"; shift
  if [[ "$AUTO_APPROVE" == "1" ]]; then
    terraform -chdir="$dir" destroy -input=false -auto-approve -var "aws_region=${AWS_REGION}" "$@"
  else
    terraform -chdir="$dir" destroy -input=false -var "aws_region=${AWS_REGION}" "$@"
  fi
}

tf_out() { terraform -chdir="$1" output -raw "$2" 2>/dev/null || true; }

# Is anything in this Terraform state? (used by status / auto-stop)
tf_has_resources() { [[ -n "$(terraform -chdir="$1" state list 2>/dev/null | grep -v '^data\.' || true)" ]]; }

# ------------------------------------------------------------------ ShipIt
TF_BOOT="$REPO_ROOT/infra/bootstrap"
TF_APP="$REPO_ROOT/infra/app"
SSM_LATEST="/shipit/release/latest"       # newest signed build (written by the release workflow)
SSM_LAST_GOOD="/shipit/release/last-good" # newest release that passed the smoke test in AWS
: "${GITHUB_OWNER:=${GITHUB_REPOSITORY_OWNER:-}}"
# Terraform needs values for these variables even when destroying.
DUMMY_IMAGE="destroy.invalid/x@sha256:0000000000000000000000000000000000000000000000000000000000000000"

# release_json <ssm name>  -> prints the JSON, or nothing if it does not exist
release_json() {
  aws ssm get-parameter --name "$1" --query Parameter.Value --output text 2>/dev/null || true
}

# put_release <ssm name> <api image> <worker image> <version>
put_release() {
  local json
  json="$(jq -cn --arg api "$2" --arg worker "$3" --arg version "$4" \
    --arg time "$(date -u +%Y-%m-%dT%H:%M:%SZ)" '{api:$api, worker:$worker, version:$version, time:$time}')"
  aws ssm put-parameter --name "$1" --type String --overwrite --value "$json" >/dev/null
}

ecr_login_for_cosign() { # lets cosign read signatures stored in ECR
  local registry="${1%%/*}"
  aws ecr get-login-password | cosign login "$registry" -u AWS --password-stdin >/dev/null 2>&1 \
    || die "cosign could not log in to $registry"
}

# verify_signature <image@digest>: only images signed by this repository's release
# workflow on main are allowed to run. SKIP_VERIFY=1 skips (not recommended).
verify_signature() {
  if [[ "${SKIP_VERIFY:-0}" == "1" ]]; then
    warn "Skipping signature check for $1 (SKIP_VERIFY=1)"
    return 0
  fi
  command -v cosign >/dev/null 2>&1 \
    || die "cosign is needed to verify images (see _setup/install-tools-wsl.sh) - or set SKIP_VERIFY=1"
  [[ -n "$GITHUB_OWNER" ]] || die "Set GITHUB_OWNER (in _setup/config.env) so the signer identity can be checked"
  [[ "$1" == *.dkr.ecr.*.amazonaws.com/* ]] && ecr_login_for_cosign "$1"
  cosign verify "$1" \
    --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
    --certificate-identity "https://github.com/${GITHUB_OWNER}/shipit-platform/.github/workflows/release.yml@refs/heads/main" \
    >/dev/null 2>&1 || die "Signature check FAILED for $1 - refusing to deploy an image CI did not sign"
  log "Signature OK: ${1##*/}"
}

app_is_up() {
  tf_init "$TF_APP" "shipit-platform/app.tfstate"
  tf_has_resources "$TF_APP"
}

# smoke_test <url> [expected version]
smoke_test() {
  local url="$1" want="${2:-}" got=""
  for _ in $(seq 1 30); do
    if curl -fsS -m 5 "$url/healthz" >/dev/null 2>&1; then
      got="$(curl -fsS -m 5 "$url/version" 2>/dev/null | jq -r .version 2>/dev/null || true)"
      if [[ -z "$want" || "$got" == "$want" ]]; then
        log "Smoke test passed: $url/healthz OK, version ${got:-?}"
        return 0
      fi
    fi
    sleep 5
  done
  warn "Smoke test failed: $url (wanted version '${want}', got '${got}')"
  return 1
}

gh_ready() { command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; }
in_ci() { [[ -n "${GITHUB_ACTIONS:-}" ]]; }

# Project accounts (new AWS sign-up) cannot let GitHub Actions log in to AWS. Their Release
# workflow pushes the signed images to GitHub's container registry instead; the laptop copies them
# into ECR (signatures and SBOMs included, same digests) and records the release.
project_account() { [[ "${AWS_ACCOUNT_TYPE:-classic}" == project ]]; }
ghcr_repo() { echo "ghcr.io/$(tr '[:upper:]' '[:lower:]' <<<"${GITHUB_OWNER:-ObaidAbdullah16}")/shipit-$1"; }

# newest_ghcr_image <api|worker>: prints "<repo>@<digest> <version>" for the image tagged "main".
newest_ghcr_image() {
  local repo digest version
  repo="$(ghcr_repo "$1")"
  digest="$(docker buildx imagetools inspect "$repo:main" --format '{{json .Manifest}}' 2>/dev/null | jq -r '.digest // empty' || true)"
  [[ "$digest" == sha256:* ]] || return 1
  version="$(docker buildx imagetools inspect "$repo@$digest" --format '{{json .Image}}' 2>/dev/null \
    | jq -r 'if has("config") then .config.Labels else ([.[]][0].config.Labels) end | .["org.opencontainers.image.version"] // empty' || true)"
  [[ -n "$version" ]] || return 1
  echo "$repo@$digest $version"
}

# wait_for_public_ghcr: GitHub publishes new packages as private. The laptop reads them without a
# GitHub login, so both must be made public once (GitHub has no API for that - it is two clicks).
wait_for_public_ghcr() {
  local owner tries=0
  owner="$(tr '[:upper:]' '[:lower:]' <<<"${GITHUB_OWNER:-ObaidAbdullah16}")"
  while ! newest_ghcr_image api >/dev/null 2>&1 || ! newest_ghcr_image worker >/dev/null 2>&1; do
    [[ -t 0 && $tries -lt 5 ]] || return 0   # not interactive, or given up: import_release_from_ghcr explains
    tries=$((tries + 1))
    echo
    log "One-time step: make the two new packages public (they hold only the public demo app):"
    echo "    https://github.com/users/$owner/packages/container/shipit-api/settings"
    echo "    https://github.com/users/$owner/packages/container/shipit-worker/settings"
    echo "  On each page: Danger Zone > Change visibility > Public > type the name > I understand..."
    read -r -p "Press Enter when both are public (Ctrl+C to stop): " _ || return 0
  done
}

# import_release_from_ghcr: newest signed build on ghcr.io -> ECR -> release record (SSM).
import_release_from_ghcr() {
  need docker cosign jq
  docker info >/dev/null 2>&1 || die "Docker Desktop must be running (it reads the images on ghcr.io) - start it and try again"
  local api_line worker_line version src dst digest api_ecr worker_ecr
  api_line="$(newest_ghcr_image api)" && worker_line="$(newest_ghcr_image worker)" || die \
    "No ShipIt images on ghcr.io yet, or they are private. Run the Release workflow (Actions > Release > Run workflow, ~15 minutes), then make both packages public once: GitHub > your profile > Packages > shipit-api / shipit-worker > Package settings > Change visibility > Public (Guide 05)."
  version="${api_line#* }"
  [[ "${worker_line#* }" == "$version" ]] || warn "api and worker images come from different builds (${version} / ${worker_line#* }) - using both anyway"
  log "Newest signed release on ghcr.io: $version"
  verify_signature "${api_line% *}"
  verify_signature "${worker_line% *}"
  bash "$REPO_ROOT/scripts/bootstrap.sh" >/dev/null
  api_ecr="$(tf_out "$TF_BOOT" api_repository_url)"; worker_ecr="$(tf_out "$TF_BOOT" worker_repository_url)"
  [[ -n "$api_ecr" && -n "$worker_ecr" ]] || die "ECR repositories not found - run: bash scripts/bootstrap.sh"
  ecr_login_for_cosign "$api_ecr"
  for pair in "${api_line% *} $api_ecr" "${worker_line% *} $worker_ecr"; do
    src="${pair% *}"; dst="${pair#* }"; digest="${src##*@}"
    if aws ecr describe-images --repository-name "${dst#*/}" --image-ids "imageDigest=$digest" >/dev/null 2>&1; then
      log "${dst##*/} $version is already in ECR"
    else
      log "Copying ${src##*/} into ECR with its signature and SBOM"
      cosign copy "$src" "$dst:$version" >/dev/null || die "cosign copy failed for $src"
    fi
  done
  # The release record points at ECR (same digests as on ghcr.io); check the copied signatures once more.
  src="${api_line% *}"; local api="$api_ecr@${src##*@}"
  src="${worker_line% *}"; local worker="$worker_ecr@${src##*@}"
  verify_signature "$api"
  verify_signature "$worker"
  put_release "$SSM_LATEST" "$api" "$worker" "$version"
  log "Release $version recorded (ECR)"
}

# Start the Release workflow on GitHub and wait for it (about 15 minutes).
run_release_workflow() {
  local repo="${GITHUB_OWNER:-ObaidAbdullah16}/shipit-platform" id="" prev
  prev="$(gh run list --repo "$repo" --workflow release.yml --limit 1 --json databaseId --jq '.[0].databaseId // empty' 2>/dev/null || true)"
  log "No signed release yet - starting the Release workflow on GitHub (builds, scans and signs the images, ~15 minutes)"
  gh workflow run release.yml --repo "$repo" --ref main >/dev/null || die "Could not start the Release workflow (is the repository published?)"
  for _ in $(seq 1 30); do
    sleep 4
    id="$(gh run list --repo "$repo" --workflow release.yml --limit 1 --json databaseId --jq '.[0].databaseId // empty' 2>/dev/null || true)"
    [[ -n "$id" && "$id" != "$prev" ]] && break
    id=""
  done
  [[ -n "$id" ]] || die "Could not find the new Release run - open https://github.com/$repo/actions"
  log "Watching run $id (Ctrl+C stops watching only; the run continues on GitHub)"
  gh run watch "$id" --repo "$repo" --exit-status --interval 30 >/dev/null || die "The Release run failed - open https://github.com/$repo/actions/runs/$id"
}

# No signed release yet (first start, or after `down --all`)? Make one.
#   classic account: the Release workflow builds, pushes to ECR and records it.
#   project account: the Release workflow builds to ghcr.io (if there is nothing yet), the laptop imports it.
ensure_release() {
  [[ -n "$(release_json "$SSM_LATEST")" ]] && return 0
  if project_account; then
    docker info >/dev/null 2>&1 || die "Docker Desktop must be running (it reads the images on ghcr.io) - start it and try again"
    if ! newest_ghcr_image api >/dev/null 2>&1 || ! newest_ghcr_image worker >/dev/null 2>&1; then
      gh_ready || die "No release on ghcr.io yet. Run Actions > Release > Run workflow on GitHub (~15 minutes), then start again."
      run_release_workflow
      wait_for_public_ghcr
    fi
    import_release_from_ghcr
    return 0
  fi
  if in_ci || ! gh_ready; then
    die "No release yet. Run Actions > Release > Run workflow (about 15 minutes), then start again."
  fi
  run_release_workflow
  [[ -n "$(release_json "$SSM_LATEST")" ]] || die "Release finished but no release record was written - check the run's logs"
  log "Release ready"
}
