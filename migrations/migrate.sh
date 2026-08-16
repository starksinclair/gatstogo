#!/bin/sh
# Applies every migrations/*.up.sql file, in filename order, that hasn't
# been applied yet -- tracked in a schema_migrations table this script
# creates on first run.
#
# The original sketch of the migrate service just looped over every
# *.up.sql file unconditionally on every startup, which works once but
# fails every run after the first against a persistent volume: 0001's bare
# CREATE TABLE/CREATE ROLE statements (no IF NOT EXISTS) error out the
# second time they're applied. Tracking applied versions is the standard
# fix (it's what every real migration tool does) and is what makes
# `docker compose up` safe to re-run against data that already exists.
#
# Runs as the Postgres bootstrap superuser (PGUSER/PGPASSWORD below), never
# as gatstogo_app/gatstogo_admin -- migration 0001 REVOKEs schema-creation
# rights from gatstogo_app, and neither restricted role should ever need
# DDL privileges.
set -eu

: "${PGHOST:=postgres}"
: "${PGUSER:=gatstogo}"
: "${PGDATABASE:=gatstogo}"
: "${MIGRATIONS_DIR:=/migrations}"
export PGPASSWORD="${PGPASSWORD:?PGPASSWORD is required}"
# Required even though only 0003_role_credentials.up.sql references them --
# failing loudly here beats a migration silently setting gatstogo_app's or
# gatstogo_admin's password to an empty string because a var was unset.
: "${GATSTOGO_APP_ROLE_PASSWORD:?GATSTOGO_APP_ROLE_PASSWORD is required}"
: "${GATSTOGO_ADMIN_ROLE_PASSWORD:?GATSTOGO_ADMIN_ROLE_PASSWORD is required}"
# Defaults to NOT seeding -- see the *_seed_data.up.sql skip below. This is
# the fail-safe direction: any deployment that forgets to set this
# explicitly gets a database with no demo accounts, never one with a
# platform-admin login whose password is published in this repo's own
# migration file.
: "${SEED_DEV_DATA:=false}"

psql_exec() {
  psql -X -q -v ON_ERROR_STOP=1 -h "$PGHOST" -U "$PGUSER" -d "$PGDATABASE" "$@"
}

until pg_isready -h "$PGHOST" -U "$PGUSER" -d "$PGDATABASE" >/dev/null 2>&1; do
  echo "waiting for postgres..."
  sleep 1
done

psql_exec -c "CREATE TABLE IF NOT EXISTS schema_migrations (
  version     text PRIMARY KEY,
  applied_at  timestamptz NOT NULL DEFAULT now()
);"

for file in "$MIGRATIONS_DIR"/*.up.sql; do
  version=$(basename "$file")

  # *_seed_data.up.sql files (currently just 0002) create known,
  # documented dev/demo login credentials -- see that file's own header
  # comment, which already said "never run this against a real production
  # database" but, before this check, had no actual technical control
  # behind that warning: this loop applied every *.up.sql file
  # unconditionally, in every environment, including production. This is
  # the real enforcement. Any *future* seed-only migration gets the same
  # protection automatically by following the same *_seed_data.up.sql
  # naming convention -- worth keeping in mind if one's ever added.
  case "$version" in
    *_seed_data.up.sql)
      if [ "$SEED_DEV_DATA" != "true" ]; then
        echo "skip $version (SEED_DEV_DATA is not \"true\" -- see this script's own comment)"
        continue
      fi
      ;;
  esac

  # :'version' substitution deliberately goes through stdin (a heredoc), not
  # -c "...". psql's -c sends its argument to the server close to verbatim
  # -- unlike a script fed via -f or stdin, it does NOT run psql's own
  # variable-interpolation pass over the SQL text, so :'version' would reach
  # Postgres literally and fail with a syntax error at ":". Confirmed by
  # actually running this against a live Postgres (docker/podman compose)
  # for the first time -- this codebase's -c "...:'version'..." form had
  # never been exercised against a real psql before.
  already=$(psql_exec -X -A -t -v version="$version" <<'SQL'
SELECT 1 FROM schema_migrations WHERE version = :'version';
SQL
  )
  if [ "$already" = "1" ]; then
    echo "skip $version (already applied)"
    continue
  fi

  echo "applying $version"
  psql_exec \
    -v app_password="$GATSTOGO_APP_ROLE_PASSWORD" \
    -v admin_password="$GATSTOGO_ADMIN_ROLE_PASSWORD" \
    -f "$file"

  psql_exec -v version="$version" <<'SQL'
INSERT INTO schema_migrations (version) VALUES (:'version');
SQL
done

echo "migrations complete"
