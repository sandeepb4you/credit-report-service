# Database backup and restore

Continuous archiving to S3 with pgBackRest. Set up 2026-09-13; before that date
**nothing was backed up at all**.

- **Recovery point (RPO): ≤ 60 seconds.** Every write-ahead-log segment is
  shipped to S3 as it fills, and `archive_timeout=60` closes a partial segment
  once a minute, so at most a minute of writes is at risk.
- **Recovery time (RTO): ~5 minutes** for this database — a 32 MB restore plus
  WAL replay took 30 seconds in the drill; the rest is you reading this file.
- **Retention: 35 days**, enforced by the bucket's lifecycle rule.

## Where it lives

| Thing | Value |
| --- | --- |
| Bucket | `myscorr-db-backups` (ap-south-1), versioned, SSE-KMS |
| Key | `alias/myscorr-db-backups`, annual rotation on |
| Repo path | `s3://myscorr-db-backups/pgbackrest` |
| Stanza | `myscorr` |
| Config | baked into the image, `deploy/db/pgbackrest.conf` |
| Image | `.../scorr-postgres:16-pgbackrest`, built from `deploy/db/Dockerfile` |
| Schedule | `myscorr-db-backup.timer`, daily 18:30 UTC (midnight IST) |
| Script | `/opt/scorr/backup.sh` (from `deploy/db/backup.sh`) |
| Log | `/var/log/pgbackrest/cron.log` on the host |

## Checking it is alive

```bash
ssh ec2-user@api.myscorr.com
sudo tail -20 /var/log/pgbackrest/cron.log          # one block per day
docker exec -u postgres scorr-db-1 pgbackrest --stanza=myscorr info
docker exec -u postgres scorr-db-1 pgbackrest --stanza=myscorr check
```

`check` is the one that matters: it pushes a WAL segment and reads it back, so
it proves the whole path rather than the last backup's existence.

**The failure mode to know.** A broken `archive_command` does not stop the
database and shows nothing a user would notice. Postgres keeps the WAL on local
disk and retries forever, so the first symptom is a full disk — days or weeks
later, with a hole in the backups the whole width of the outage. That is what
the daily `check` is for. Also worth watching:

```sql
SELECT last_archived_wal, last_failed_wal, failed_count FROM pg_stat_archiver;
```

## Restoring

### The whole cluster, to the latest moment

Stops the API first so nothing writes to a database that is about to be replaced.

```bash
cd /opt/scorr
docker compose stop api
docker compose stop db
sudo rm -rf /var/lib/docker/volumes/scorr_pgdata/_data/*        # see warning below
docker run --rm -u postgres -v scorr_pgdata:/var/lib/postgresql/data \
  $ECR_REGISTRY/scorr-postgres:16-pgbackrest \
  pgbackrest --stanza=myscorr --pg1-path=/var/lib/postgresql/data restore
docker compose up -d db      # Postgres replays the WAL on start
docker compose up -d api
```

> **The `rm -rf` is the dangerous line in this file.** pgBackRest refuses to
> restore over a non-empty directory without `--delta` or `--force`, and that
> refusal is a feature. Read the path twice. If you are not certain, restore to
> a scratch directory first (below) and look at it.

### To a point in time — "just before the bad migration"

```bash
docker run --rm -u postgres -v scorr_pgdata:/var/lib/postgresql/data \
  $ECR_REGISTRY/scorr-postgres:16-pgbackrest \
  pgbackrest --stanza=myscorr --pg1-path=/var/lib/postgresql/data \
    --type=time --target="2026-09-13 13:44:00+00" --target-action=promote restore
```

The target is a timestamp **with a timezone**; the server thinks in UTC. After
promotion the timeline diverges, and the WAL from the abandoned timeline stays
in the repo, so a second attempt at a different target is still possible.

### Somewhere safe, to look before you leap

This is the drill that was run when the backups were set up, and it is the right
first move in almost every real incident — it touches nothing live:

```bash
sudo rm -rf /tmp/pgrestore && sudo mkdir -p /tmp/pgrestore && sudo chown 70:70 /tmp/pgrestore
docker run --rm -u postgres -v /tmp/pgrestore:/var/lib/postgresql/data \
  $ECR_REGISTRY/scorr-postgres:16-pgbackrest \
  pgbackrest --stanza=myscorr --pg1-path=/var/lib/postgresql/data restore
docker run -d --name pgcheck -u postgres -v /tmp/pgrestore:/var/lib/postgresql/data \
  -p 127.0.0.1:5433:5432 $ECR_REGISTRY/scorr-postgres:16-pgbackrest postgres -c archive_mode=off
docker exec pgcheck psql -U scorr -d credit -c "SELECT count(*) FROM report.accounts;"
docker rm -f pgcheck && sudo rm -rf /tmp/pgrestore
```

`archive_mode=off` on the scratch copy is not optional: a restored cluster with
archiving on will push its own WAL into the live repository and confuse the
timeline everything else restores from.

## Decisions worth knowing before you change anything

**Every base backup is FULL, never incremental.** Retention is the bucket's
lifecycle rule, and a lifecycle rule cannot tell that a 30-day-old full backup
is still the ancestor of yesterday's incremental — it would expire the full and
leave a chain that restores to nothing. At 32 MB a full backup costs 4 MB
compressed and 40 seconds, so the safe shape is also the cheap one. If this
database grows past a few GB, revisit: that is the point where incremental is
worth the bookkeeping, and where `repo1-retention-full` (with delete permission)
should take over from lifecycle.

**The instance cannot destroy its own backups.** The bucket policy denies
`s3:DeleteObjectVersion` to the `scorr-ec2` role, so someone who takes the box
cannot erase what is in S3 — deletes only create delete markers, and every
version survives until lifecycle expires it. `s3:DeleteObject` *is* allowed,
because pgBackRest replaces a `latest` pointer on every backup and cannot work
without it. The trade: a compromised box can hide the backups, but not destroy
them; an admin can strip the delete markers.

**`pgbackrest expire` does nothing here**, and says so in the log
(`option 'repo1-retention-archive' is not set`). That line is expected. Retention
belongs to the lifecycle rule for the reason above.

**`pgbackrest info` can list backups S3 has already expired.** Anything inside 35
days is real; older entries are the manifest remembering what lifecycle removed.

## What is NOT backed up by this

- **The S3 report PDFs and uploaded PAN cards.** They live in
  `myscorr-credit-reports`, which is versioned — that protects against overwrite
  and delete, not against the bucket being lost. They are re-derivable (the
  advanced report renders from the database; the bureau PDF is not), so the
  database is the thing that matters most, but this is a real gap.
- **The box itself** — nginx config, certificates, `/opt/scorr/.env`. Rebuilding
  it is a documented job (`deploy/README.md`), not a restore.
