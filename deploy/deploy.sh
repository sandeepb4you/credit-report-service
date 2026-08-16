#!/usr/bin/env bash
# One-shot backend deploy: build the API image for linux/amd64, push it to ECR,
# then pull + restart the api container on the server and verify /api/ping.
#
#   ./deploy/deploy.sh
#
# Requires: aws cli logged in with ECR push rights, docker buildx, ssh access
# to the server. Override any of the env vars below inline, e.g.
#   DEPLOY_SERVER=ubuntu@1.2.3.4 ./deploy/deploy.sh
set -euo pipefail

AWS_REGION="${AWS_REGION:-ap-south-1}"
ECR_REPO="${ECR_REPO:-scorr-api}"
DEPLOY_SERVER="${DEPLOY_SERVER:-ec2-user@api.myscorr.com}"
REMOTE_DIR="${REMOTE_DIR:-/opt/scorr}"
HEALTH_URL="${HEALTH_URL:-https://api.myscorr.com/api/ping}"

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

echo "==> Restarting api on $DEPLOY_SERVER"
ssh "$DEPLOY_SERVER" bash -s <<EOF
set -euo pipefail
aws ecr get-login-password --region "$AWS_REGION" |
    docker login --username AWS --password-stdin "$ECR_REGISTRY"
cd "$REMOTE_DIR"
docker compose pull api
docker compose up -d api
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
