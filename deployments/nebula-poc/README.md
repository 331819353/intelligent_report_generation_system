# AskData NebulaGraph compatibility POC

This directory is the isolated `GRAPH-001` harness. It does not modify or
extend the repository-root production-style Compose topology.

## Locked compatibility decision

- NebulaGraph server images: `v3.8.0` (`metad`, `storaged`, and `graphd`).
- Go client: `github.com/vesoft-inc/nebula-go/v3 v3.8.0`.
- Protocol: the v3 client uses fbthrift on graphd port `9669`.
- Do not substitute `master`, nightly, or `nebula-go/v5`. The v5 module is a
  different gRPC client for the newer Nebula service and is not the v3 graphd
  client selected by the technical design.

The version pair follows the [NebulaGraph 3.8 Go client documentation](https://docs.nebula-graph.io/3.8.0/14.client/6.nebula-go-client/)
and the official [NebulaGraph Go Client v3.8.0 release](https://github.com/vesoft-inc/nebula-go/releases/tag/v3.8.0).
The image topology and health endpoints follow the official
[NebulaGraph v3.8.0 Docker Compose](https://github.com/vesoft-inc/nebula-docker-compose/blob/v3.8.0/docker-compose.yaml).

## Run

```sh
./scripts/verify-nebula-poc.sh
```

The script generates one-day test certificates in a temporary directory,
starts an ephemeral single-meta/single-storage stack with two plain graphd
instances, one TLS graphd instance, and a TCP blackhole, then runs the Go POC.
The test verifies the server version, connection and Session Pool behavior,
Space binding, parameter escaping, socket timeout, concurrent use, TLS, and
recovery after one graphd instance is stopped. All POC data uses `tmpfs`, and
the script removes only the explicit `askdata-nebula-poc` Compose project.
