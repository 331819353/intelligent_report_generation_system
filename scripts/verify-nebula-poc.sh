#!/usr/bin/env bash
set -euo pipefail
export COMPOSE_PROGRESS=${COMPOSE_PROGRESS:-plain}

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
compose_file="$repo_root/deployments/nebula-poc/compose.yaml"
poc_project=askdata-nebula-poc
poc_temp_dir=$(mktemp -d "${TMPDIR:-/tmp}/askdata-nebula-poc.XXXXXX")
cert_dir="$poc_temp_dir/certs"
mkdir -p "$cert_dir"

cleanup() {
  if [ "${ASKDATA_NEBULA_POC_KEEP:-0}" = "1" ]; then
    return
  fi
  ASKDATA_NEBULA_POC_CERT_DIR="$cert_dir" \
    docker compose --project-name "$poc_project" --file "$compose_file" down --remove-orphans >/dev/null 2>&1 || true
  rm -rf -- "$poc_temp_dir"
}
trap cleanup EXIT INT TERM

openssl req -x509 -newkey rsa:2048 -sha256 -days 1 -nodes \
  -keyout "$cert_dir/ca.key" -out "$cert_dir/ca.crt" \
  -subj '/CN=AskData Nebula POC CA' >/dev/null 2>&1
openssl req -new -newkey rsa:2048 -sha256 -nodes \
  -keyout "$cert_dir/server.key" -out "$cert_dir/server.csr" \
  -subj '/CN=localhost' \
  -addext 'subjectAltName=DNS:localhost,DNS:nebula-graphd-tls,IP:127.0.0.1' >/dev/null 2>&1
openssl x509 -req -sha256 -days 1 -in "$cert_dir/server.csr" \
  -CA "$cert_dir/ca.crt" -CAkey "$cert_dir/ca.key" -CAcreateserial \
  -copy_extensions copy -out "$cert_dir/server.crt" >/dev/null 2>&1
openssl req -new -newkey rsa:2048 -sha256 -nodes \
  -keyout "$cert_dir/client.key" -out "$cert_dir/client.csr" \
  -subj '/CN=askdata-nebula-poc-client' >/dev/null 2>&1
openssl x509 -req -sha256 -days 1 -in "$cert_dir/client.csr" \
  -CA "$cert_dir/ca.crt" -CAkey "$cert_dir/ca.key" -CAcreateserial \
  -out "$cert_dir/client.crt" >/dev/null 2>&1
chmod 600 "$cert_dir"/*.key

export ASKDATA_NEBULA_POC_CERT_DIR="$cert_dir"
docker compose --project-name "$poc_project" --file "$compose_file" up --detach --wait

export ASKDATA_NEBULA_POC_INTEGRATION=1
export ASKDATA_NEBULA_POC_ADDRESSES=127.0.0.1:19669,127.0.0.1:29669
export ASKDATA_NEBULA_POC_TLS_ADDRESS=127.0.0.1:39669
export ASKDATA_NEBULA_POC_BLACKHOLE_ADDRESS=127.0.0.1:49669
export ASKDATA_NEBULA_POC_CA_FILE="$cert_dir/ca.crt"
export ASKDATA_NEBULA_POC_CLIENT_CERT_FILE="$cert_dir/client.crt"
export ASKDATA_NEBULA_POC_CLIENT_KEY_FILE="$cert_dir/client.key"
export ASKDATA_NEBULA_POC_COMPOSE_FILE="$compose_file"
export ASKDATA_NEBULA_POC_COMPOSE_PROJECT="$poc_project"
export ASKDATA_NEBULA_POC_FAILURE_RECOVERY=1

go test ./internal/askdata/graph -run 'TestNebula(GraphCompatibilityPOC|VersionLock)$' -count=1 -v
