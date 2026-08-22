#!/usr/bin/env bash
# One-shot backend deploy: build the API image for linux/amd64, push it to ECR,
# then pull + restart the api container on the server and verify /api/ping.
#
#   ./deploy/deploy.sh
#
# Requires: aws cli logged in with ECR push rights, docker buildx, ssh access
# to the server. Override any of the env vars below inline, e.g.
#   DEPLOY_SERVER=ubuntu@1.2.3.4 ./deploy/deploy.sh
#
# The image is only half a deploy: anything configured in docker-compose.yml or
# the env file (ports, volumes, every SMS_/CASHFREE_/MAIL_ setting) reaches the
# server through those two files, not through the image. They are synced here so
# a config change cannot be left behind by a deploy that reports success.
# SYNC_CONFIG=0 skips that step for an image-only redeploy.
set -euo pipefail

AWS_REGION="${AWS_REGION:-ap-south-1}"
ECR_REPO="${ECR_REPO:-scorr-api}"
DEPLOY_SERVER="${DEPLOY_SERVER:-ec2-user@api.myscorr.com}"
REMOTE_DIR="${REMOTE_DIR:-/opt/scorr}"
HEALTH_URL="${HEALTH_URL:-https://api.myscorr.com/api/ping}"
SYNC_CONFIG="${SYNC_CONFIG:-1}"
# Local env file that becomes the server's /opt/scorr/.env. Holds real secrets,
# so it is gitignored — see deploy/.env.example for the shape.
ENV_FILE="${ENV_FILE:-$(cd "$(dirname "$0")" && pwd)/.env.staging}"

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

ACCOUNT_ID="$(aws sts get-caller-identity --query Account --output text)"
ECR_REGISTRY="$ACCOUNT_ID.dkr.ecr.$AWS_REGION.amazonaws.com"
IMAGE="$ECR_REGISTRY/$ECR_REPO"

TAG="$(git -C "$REPO_ROOT" rev-parse --short HEAD)"
if ! git -C "$REPO_ROOT" diff --quiet HEAD --; then
    TAG="$TAG-dirty"
    echo "NOTE: working tree has uncommitted changes; tagging as $TAG"
fi

echo "==> ECR login ($ECR_REGISTRY)"
aws ecr get-login-password --region "$AWS_REGION" |
    docker login --username AWS --password-stdin "$ECR_REGISTRY"

echo "==> Building and pushing $IMAGE:$TAG"
docker buildx build --platform linux/amd64 \
    -t "$IMAGE:$TAG" -t "$IMAGE:latest" \
    --push "$REPO_ROOT"

if [ "$SYNC_CONFIG" = "1" ]; then
    echo "==> Syncing docker-compose.yml + env to $DEPLOY_SERVER:$REMOTE_DIR"
    if [ ! -f "$ENV_FILE" ]; then
        echo "!! ENV_FILE not found: $ENV_FILE" >&2
        echo "   Copy deploy/.env.example, fill it in, or pass ENV_FILE=/path/to/env." >&2
        echo "   SYNC_CONFIG=0 ./deploy/deploy.sh deploys the image only." >&2
        exit 1
    fi
    scp -q "$REPO_ROOT/deploy/docker-compose.yml" "$DEPLOY_SERVER:$REMOTE_DIR/docker-compose.yml"
    # Back the old env up before replacing it. A rollback pins IMAGE_TAG in the
    # server's copy (see README section 6), and overwriting that silently would
    # undo the rollback while reporting a clean deploy.
    ssh "$DEPLOY_SERVER" "cd '$REMOTE_DIR' && if [ -f .env ]; then cp -p .env .env.bak; fi"
    scp -q "$ENV_FILE" "$DEPLOY_SERVER:$REMOTE_DIR/.env"
    # Secrets: readable by the owner only. scp would otherwise apply the umask.
    ssh "$DEPLOY_SERVER" "chmod 600 '$REMOTE_DIR/.env'"
    echo "    synced $(basename "$ENV_FILE") -> $REMOTE_DIR/.env (previous kept as .env.bak)"
fi

echo "==> Restarting api on $DEPLOY_SERVER"
ssh "$DEPLOY_SERVER" bash -s <<EOF
set -euo pipefail
aws ecr get-login-password --region "$AWS_REGION" |
    docker login --username AWS --password-stdin "$ECR_REGISTRY"
cd "$REMOTE_DIR"
docker compose pull api
# Whole stack, not just api: a synced compose file can change the db service
# too, and "up -d api" would leave that half-applied.
docker compose up -d
docker image prune -f >/dev/null
EOF

echo "==> Waiting for $HEALTH_URL"
for _ in $(seq 1 15); do
    if curl -fsS --max-time 3 "$HEALTH_URL" >/dev/null 2>&1; then
        echo "==> Deployed $TAG"
        exit 0
    fi
    sleep 2
done

echo "!! Health check failed. Inspect with:" >&2
echo "   ssh $DEPLOY_SERVER 'cd $REMOTE_DIR && docker compose logs --tail=100 api'" >&2
exit 1
