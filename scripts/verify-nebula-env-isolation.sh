#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
helper="$ROOT_DIR/scripts/run-with-nebula-role.sh"

run_fixture() {
  role=$1
  shift
  env \
    ASKDATA_NEBULA_ADDRESSES=graph.example:9669 \
    ASKDATA_NEBULA_SPACE=graph_fixture \
    ASKDATA_NEBULA_USERNAME=unexpected \
    ASKDATA_NEBULA_PASSWORD=unexpected \
    ASKDATA_NEBULA_TLS_ENABLED=false \
    ASKDATA_NEBULA_PORT=9669 \
    ASKDATA_NEBULA_ROOT_PASSWORD=fixture_root \
    ASKDATA_NEBULA_BOOTSTRAP_ROOT_PASSWORD=fixture_bootstrap \
    ASKDATA_NEBULA_API_USER=fixture_reader \
    ASKDATA_NEBULA_API_PASSWORD=fixture_reader_password \
    ASKDATA_NEBULA_WORKER_USER=fixture_writer \
    ASKDATA_NEBULA_WORKER_PASSWORD=fixture_writer_password \
    "$helper" "$role" /bin/sh -ec "$*"
}

canonical_secrets_absent='
  test -z "${ASKDATA_NEBULA_ROOT_PASSWORD:-}"
  test -z "${ASKDATA_NEBULA_BOOTSTRAP_ROOT_PASSWORD:-}"
  test -z "${ASKDATA_NEBULA_API_USER:-}"
  test -z "${ASKDATA_NEBULA_API_PASSWORD:-}"
  test -z "${ASKDATA_NEBULA_WORKER_USER:-}"
  test -z "${ASKDATA_NEBULA_WORKER_PASSWORD:-}"
  test -z "${ASKDATA_NEBULA_PORT:-}"
'

run_fixture api "
  test \"\$ASKDATA_NEBULA_USERNAME\" = fixture_reader
  test \"\$ASKDATA_NEBULA_PASSWORD\" = fixture_reader_password
  test \"\$ASKDATA_NEBULA_ADDRESSES\" = graph.example:9669
  test \"\$ASKDATA_NEBULA_SPACE\" = graph_fixture
  $canonical_secrets_absent
"

run_fixture worker "
  test \"\$ASKDATA_NEBULA_USERNAME\" = fixture_writer
  test \"\$ASKDATA_NEBULA_PASSWORD\" = fixture_writer_password
  test \"\$ASKDATA_NEBULA_ADDRESSES\" = graph.example:9669
  test \"\$ASKDATA_NEBULA_SPACE\" = graph_fixture
  $canonical_secrets_absent
"

run_fixture none "
  test -z \"\${ASKDATA_NEBULA_ADDRESSES:-}\"
  test -z \"\${ASKDATA_NEBULA_SPACE:-}\"
  test -z \"\${ASKDATA_NEBULA_USERNAME:-}\"
  test -z \"\${ASKDATA_NEBULA_PASSWORD:-}\"
  test -z \"\${ASKDATA_NEBULA_TLS_ENABLED:-}\"
  $canonical_secrets_absent
"

printf 'local NebulaGraph credential isolation passed\n'
