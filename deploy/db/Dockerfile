# Postgres with pgBackRest, for continuous archiving to S3.
#
# The stock postgres:16-alpine image with one package added. Deliberately NOT a
# switch to the Debian-based image, which is where the obvious "just use the
# glibc build of the backup tool" road leads: the data directory was initialised
# under musl, and moving an existing cluster between libc implementations
# changes collation order, which silently corrupts every index on a text column.
# pgBackRest is in Alpine's community repo, so nothing has to move.
#
# Why pgBackRest rather than a dump on a timer: WAL archiving is what makes the
# recovery point seconds instead of hours. Every write-ahead-log segment is
# shipped to S3 as it is filled (and at least once a minute, see
# archive_timeout), so a restore can land on any moment between the last base
# backup and the failure.
FROM postgres:16-alpine

RUN apk add --no-cache pgbackrest

# The repository is defined in the image rather than in the environment because
# it contains no secrets: credentials come from the EC2 instance role through
# IMDS (repo1-s3-key-type=auto), so there is nothing here to leak and one less
# thing for a deploy to get wrong.
COPY pgbackrest.conf /etc/pgbackrest/pgbackrest.conf

RUN mkdir -p /var/log/pgbackrest /var/lib/pgbackrest \
 && chown -R postgres:postgres /var/log/pgbackrest /var/lib/pgbackrest /etc/pgbackrest
