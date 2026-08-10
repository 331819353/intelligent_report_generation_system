#!/usr/bin/env sh
set -eu

usage() {
  echo "usage: $0 --backup DIR --confirm-empty-target [--database-url URL]" >&2
  exit 2
}

backup_dir=
confirmed=false
database_url=${ASKDATA_CONTROL_DATABASE_URL:-${WORKER_DATABASE_URL:-}}
while [ "$#" -gt 0 ]; do
  case "$1" in
    --backup) [ "$#" -ge 2 ] || usage; backup_dir=$2; shift 2 ;;
    --database-url) [ "$#" -ge 2 ] || usage; database_url=$2; shift 2 ;;
    --confirm-empty-target) confirmed=true; shift ;;
    *) usage ;;
  esac
done
[ -n "$backup_dir" ] && [ -n "$database_url" ] && [ "$confirmed" = true ] || usage
archive="$backup_dir/askdata.dump"
expected_manifest="$backup_dir/release-manifest.tsv"
[ -f "$archive" ] && [ -f "$expected_manifest" ] && [ -f "$backup_dir/SHA256SUMS" ] || {
  echo "backup directory is incomplete: $backup_dir" >&2
  exit 1
}
command -v pg_restore >/dev/null 2>&1 || { echo 'pg_restore is required' >&2; exit 1; }
command -v psql >/dev/null 2>&1 || { echo 'psql is required' >&2; exit 1; }

if command -v sha256sum >/dev/null 2>&1; then
  (cd "$backup_dir" && sha256sum -c SHA256SUMS)
else
  (cd "$backup_dir" && shasum -a 256 -c SHA256SUMS)
fi
pg_restore --list "$archive" >/dev/null

# The control database is backed up as a referentially closed unit because the
# semantic registry references platform tenants/users and report/evaluation
# records reference semantic objects. Refuse a non-empty target rather than
# dropping unrelated data.
user_table_count=$(psql --dbname="$database_url" -X -v ON_ERROR_STOP=1 -At -c "
SELECT count(*) FROM pg_catalog.pg_class AS relation
JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid=relation.relnamespace
WHERE relation.relkind IN ('r','p')
  AND namespace.nspname NOT IN ('pg_catalog','information_schema')")
[ "$user_table_count" = 0 ] || {
  echo 'restore target is not empty; refusing to overwrite it' >&2
  exit 1
}
PGDATABASE=$database_url pg_restore \
  --no-owner --no-privileges --exit-on-error --dbname="$database_url" "$archive"

actual_manifest=$(mktemp "${TMPDIR:-/tmp}/askdata-release-manifest.XXXXXX")
trap 'rm -f -- "$actual_manifest"' EXIT INT TERM
psql --dbname="$database_url" -X -v ON_ERROR_STOP=1 -At -F '	' -c "
SELECT release.tenant_id,release.domain_id,release.id,release.semantic_version,
       release.status,release.object_count,release.content_hash,
       askdata.release_manifest_hash(release.id)
FROM askdata.releases AS release
ORDER BY release.tenant_id,release.domain_id,release.id" >"$actual_manifest"
if ! cmp -s "$expected_manifest" "$actual_manifest"; then
  echo 'restored release manifest differs from the backup; target is not accepted' >&2
  diff -u "$expected_manifest" "$actual_manifest" >&2 || true
  exit 1
fi
printf 'AskData PostgreSQL restore verified: %s\n' "$backup_dir"
