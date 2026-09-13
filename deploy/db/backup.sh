#!/usr/bin/env bash
# Daily base backup, run from cron on the EC2 host (see deploy/db/README.md).
#
# The WAL is already leaving continuously through Postgres's archive_command —
# that is what makes the recovery point seconds rather than a day. This script
# only takes the periodic base backup the WAL is replayed on top of, and then
# asks pgBackRest to prove archiving still works.
#
# `check` is the part worth having. A broken archive_command does not stop the
# database or raise anything a user would see: Postgres simply keeps the WAL on
# local disk and retries, and the first symptom is a full disk days later, by
# which time the backups have a days-wide hole in them. This runs every day and
# says so in the log.
set -uo pipefail

STANZA=myscorr
CONTAINER=scorr-db-1
LOG=/var/log/pgbackrest/cron.log

log() { echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) $*" >>"$LOG"; }

run() {
    docker exec -u postgres "$CONTAINER" pgbackrest --stanza="$STANZA" "$@" 2>&1
}

log "=== backup start ==="
if out=$(run --type=full backup); then
    log "backup ok: $(echo "$out" | grep -E 'new backup label|backup size' | tr '\n' ' ')"
else
    log "BACKUP FAILED (exit $?)"
    echo "$out" | tail -20 >>"$LOG"
fi

if out=$(run check); then
    log "check ok: archiving verified end to end"
else
    log "CHECK FAILED — WAL is not reaching S3; the recovery point is frozen at the last good segment"
    echo "$out" | tail -20 >>"$LOG"
fi

# Surface the two numbers worth seeing at a glance in the daily line.
log "repo: $(run info | grep -E 'backup set size' | tail -1 | tr -s ' ')"
log "=== backup end ==="
