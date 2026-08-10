#!/usr/bin/env sh
set -eu

usage() {
  echo "usage: $0 --output DIR [--database-url URL]" >&2
  exit 2
}

output_dir=
database_url=${ASKDATA_CONTROL_DATABASE_URL:-${WORKER_DATABASE_URL:-}}
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output) [ "$#" -ge 2 ] || usage; output_dir=$2; shift 2 ;;
    --database-url) [ "$#" -ge 2 ] || usage; database_url=$2; shift 2 ;;
    *) usage ;;
  esac
done
[ -n "$output_dir" ] && [ -n "$database_url" ] || usage
command -v pg_dump >/dev/null 2>&1 || { echo 'pg_dump is required' >&2; exit 1; }
command -v pg_restore >/dev/null 2>&1 || { echo 'pg_restore is required' >&2; exit 1; }
command -v psql >/dev/null 2>&1 || { echo 'psql is required' >&2; exit 1; }

if [ -e "$output_dir" ] && [ -n "$(find "$output_dir" -mindepth 1 -maxdepth 1 -print -quit 2>/dev/null)" ]; then
  echo "backup output directory must be empty: $output_dir" >&2
  exit 1
fi
mkdir -p -m 700 "$output_dir"
archive="$output_dir/askdata.dump"
manifest="$output_dir/release-manifest.tsv"
inventory="$output_dir/table-inventory.tsv"

pg_dump --dbname="$database_url" \
  --format=custom --compress=9 --no-owner --no-privileges \
  --file="$archive"
pg_restore --list "$archive" >/dev/null

psql --dbname="$database_url" -X -v ON_ERROR_STOP=1 -At -F '	' -c "
SELECT release.tenant_id,release.domain_id,release.id,release.semantic_version,
       release.status,release.object_count,release.content_hash,
       askdata.release_manifest_hash(release.id)
FROM askdata.releases AS release
ORDER BY release.tenant_id,release.domain_id,release.id" >"$manifest"

psql --dbname="$database_url" -X -v ON_ERROR_STOP=1 -At -F '	' -c "
SELECT table_name,
       (xpath('/row/count/text()',query_to_xml(
         format('SELECT count(*) AS count FROM askdata.%I',table_name),false,true,''
       )))[1]::text::bigint
FROM information_schema.tables
WHERE table_schema='askdata' AND table_type='BASE TABLE'
ORDER BY table_name" >"$inventory"

if command -v sha256sum >/dev/null 2>&1; then
  (cd "$output_dir" && sha256sum askdata.dump release-manifest.tsv table-inventory.tsv >SHA256SUMS)
else
  (cd "$output_dir" && shasum -a 256 askdata.dump release-manifest.tsv table-inventory.tsv >SHA256SUMS)
fi
chmod 600 "$archive" "$manifest" "$inventory" "$output_dir/SHA256SUMS"
printf 'AskData PostgreSQL backup completed: %s\n' "$output_dir"
