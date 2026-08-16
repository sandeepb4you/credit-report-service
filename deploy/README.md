# Deploying scorr on EC2

One Ubuntu box runs everything: nginx on the host (TLS, routing), and this
folder's docker-compose stack (api + postgres) at `/opt/scorr`.

```
api.myscorr.com  -> nginx -> 127.0.0.1:8080 (api container, image from ECR)
www.myscorr.com  -> nginx -> /var/www/myscorr (static Kotlin/JS app)
myscorr.com      -> 301 -> www.myscorr.com
db               -> compose-internal; 127.0.0.1:5432 on the host for SSH-tunnel admin
```

Day-to-day deploys:

- **Backend**: `./deploy/deploy.sh` (this repo) — builds, pushes to ECR, restarts the container, checks `/api/ping`.
- **Web**: `./deploy-web.sh` (scorrclub repo) — gradle build + rsync.

Everything below is one-time setup.

## 1. AWS pieces

```sh
aws ecr create-repository --repository-name scorr-api --region ap-south-1

# keep only the 10 most recent images
aws ecr put-lifecycle-policy --repository-name scorr-api --region ap-south-1 \
  --lifecycle-policy-text '{"rules":[{"rulePriority":1,"selection":{"tagStatus":"any","countType":"imageCountMoreThan","countNumber":10},"action":{"type":"expire"}}]}'
```

- EC2: Amazon Linux 2023, `t2.medium` (amd64 — deploy.sh builds `linux/amd64`), Elastic IP. Login user is `ec2-user`.
- Security group: 22 (your IP), 80, 443. Nothing else — Postgres stays closed.
- Instance IAM role with `AmazonEC2ContainerRegistryReadOnly` so the server can
  `docker compose pull` without stored AWS keys.
- DNS A records: `api`, `www`, and apex `myscorr.com` -> the Elastic IP.

## 2. Server bootstrap (Amazon Linux 2023)

```sh
sudo dnf install -y docker nginx certbot python3-certbot-nginx rsync
sudo systemctl enable --now docker nginx

# compose v2 is a CLI plugin, not a dnf package
sudo mkdir -p /usr/local/lib/docker/cli-plugins
sudo curl -SL https://github.com/docker/compose/releases/latest/download/docker-compose-linux-x86_64 \
  -o /usr/local/lib/docker/cli-plugins/docker-compose
sudo chmod +x /usr/local/lib/docker/cli-plugins/docker-compose

sudo usermod -aG docker ec2-user      # re-login after this
sudo mkdir -p /opt/scorr /var/www/myscorr
sudo chown ec2-user:ec2-user /opt/scorr /var/www/myscorr
```

The aws CLI is preinstalled on AL2023 (used only for the ECR pull, authorized
by the instance role — no keys to configure).

## 3. Install this folder on the server

From your machine:

```sh
scp -r deploy/docker-compose.yml deploy/init ec2-user@api.myscorr.com:/opt/scorr/
scp deploy/.env.staging ec2-user@api.myscorr.com:/opt/scorr/.env   # or .env.example, filled in
scp deploy/nginx/*.conf ec2-user@api.myscorr.com:/tmp/
```

On the server (AL2023 nginx has no sites-enabled — drop straight into conf.d):

```sh
sudo mv /tmp/api.myscorr.com.conf /tmp/www.myscorr.com.conf /etc/nginx/conf.d/
sudo nginx -t && sudo systemctl reload nginx
sudo certbot --nginx -d api.myscorr.com -d www.myscorr.com -d myscorr.com
# AL2023 does not start the renewal timer by itself (Ubuntu does):
sudo systemctl enable --now certbot-renew.timer
```

Certbot needs the DNS records live first. After that, the timer renews
automatically — verify with `systemctl list-timers certbot-renew.timer`.

## 4. First deploy

```sh
./deploy/deploy.sh                                   # backend (this repo)
cd ~/AndroidStudioProjects/scorrclub && ./deploy-web.sh   # web
```

The db container creates the `report` schema on first boot and the api applies
its embedded migrations on startup — the stack comes up with a fresh, fully
migrated database.

## 5. Operations

- **Admin DB access** (never open 5432 in the security group):
  `ssh -N -L 5433:localhost:5432 ec2-user@api.myscorr.com`, then connect to
  `localhost:5433`. GUI clients (TablePlus/DBeaver/pgAdmin) can do this tunnel
  natively from their connection dialog.
- **Rollback**: set `IMAGE_TAG=<git-sha>` in `/opt/scorr/.env`, then
  `docker compose up -d api`. `deploy.sh` pushes every SHA, so any prior tag works.
- **Logs**: `docker compose logs -f api` in `/opt/scorr`.
- **Backups** (the pgdata volume is not a backup) — `crontab -e` on the server:

  ```
  0 3 * * * docker compose -f /opt/scorr/docker-compose.yml exec -T db pg_dump -U scorr -d credit | gzip > /home/ec2-user/backups/credit-$(date +\%F).sql.gz
  ```

  Keep `~/backups` tidy with a weekly `find -mtime +14 -delete`, or ship to S3.
