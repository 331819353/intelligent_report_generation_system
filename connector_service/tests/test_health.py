from fastapi.testclient import TestClient
from unittest.mock import patch
import asyncio
import ipaddress
import json
import os
import subprocess
import sys
import pytest
from pydantic import ValidationError

import app.main as connector_main
from app.main import ConnectionConfig, ConnectionPool, EgressDeniedError, EgressRule, MetadataSampleRequest, StreamQueryRequest, app, canonical_type, open_connection, oracle_dsn, query, resolve_egress_target, stream_query_events, validate_read_only_sql


def test_live() -> None:
    response = TestClient(app).get("/health/live")
    assert response.status_code == 200
    assert response.json() == {"status": "live"}


def test_internal_auth_required() -> None:
    response = TestClient(app).post("/v1/connections/test", json={})
    assert response.status_code == 401


def test_metadata_sync_requires_internal_auth() -> None:
    response = TestClient(app).post("/v1/metadata/sync", json={})
    assert response.status_code == 401


def test_connection_test_token_cannot_call_other_connector_capabilities() -> None:
    client = TestClient(app)
    headers = {
        "X-Connector-Token": "local_connector_connection_test_token_change_me"
    }
    # 422 proves the dedicated token passed authentication on its sole endpoint
    # and request-shape validation ran.
    assert client.post(
        "/v1/connections/test", headers=headers, json={}
    ).status_code == 422
    assert client.post(
        "/v1/metadata/sync", headers=headers, json={}
    ).status_code == 401
    assert client.post(
        "/v1/query", headers=headers, json={}
    ).status_code == 401


def test_production_connector_rejects_missing_general_token() -> None:
    environment = os.environ.copy()
    environment["APP_ENV"] = "production"
    environment.pop("CONNECTOR_INTERNAL_TOKEN", None)
    environment["CONNECTOR_CONNECTION_TEST_TOKEN"] = (
        "production-connection-test-token"
    )
    result = subprocess.run(
        [sys.executable, "-c", "import app.main"],
        env=environment,
        cwd=os.path.dirname(os.path.dirname(__file__)),
        capture_output=True,
        text=True,
        check=False,
    )
    assert result.returncode != 0
    assert "general connector token is required" in result.stderr


def production_connector_environment() -> dict[str, str]:
    environment = os.environ.copy()
    environment.update({
        "APP_ENV": "production",
        "CONNECTOR_INTERNAL_TOKEN": "production-general-connector-token",
        "CONNECTOR_CONNECTION_TEST_TOKEN": "production-connection-test-token",
        "CONNECTOR_MAX_POOLS": "1000",
        "CONNECTOR_MAX_TOTAL_CONNECTIONS": "100",
        "CONNECTOR_HTTP_MAX_REQUEST_BYTES": "1048576",
        "CONNECTOR_JSON_MAX_RESPONSE_BYTES": "67108864",
        "CONNECTOR_METADATA_SYNC_MAX_ROWS": "200000",
        "CONNECTOR_METADATA_SAMPLE_MAX_CELL_BYTES": "16384",
        "CONNECTOR_METADATA_SAMPLE_MAX_ROW_BYTES": "65536",
        "CONNECTOR_METADATA_SAMPLE_MAX_RESPONSE_BYTES": "524288",
        "CONNECTOR_STREAM_MAX_CELL_BYTES": "1048576",
        "CONNECTOR_STREAM_MAX_ROW_BYTES": "4194304",
        "CONNECTOR_STREAM_MAX_BYTES": "1073741824",
        "CONNECTOR_EGRESS_ALLOWLIST": "10.40.0.0/24:3306",
        "CONNECTOR_EGRESS_DENYLIST": "10.0.0.0/24",
    })
    return environment


def test_production_connector_rejects_hostname_only_egress_allowlist() -> None:
    environment = production_connector_environment()
    environment["CONNECTOR_EGRESS_ALLOWLIST"] = "database.internal:3306"
    result = subprocess.run(
        [sys.executable, "-c", "import app.main"],
        env=environment,
        cwd=os.path.dirname(os.path.dirname(__file__)),
        capture_output=True,
        text=True,
        check=False,
    )
    assert result.returncode != 0
    assert "accepts only IP/CIDR" in result.stderr


def test_production_connector_requires_control_plane_denylist() -> None:
    environment = production_connector_environment()
    environment.pop("CONNECTOR_EGRESS_DENYLIST")
    result = subprocess.run(
        [sys.executable, "-c", "import app.main"],
        env=environment,
        cwd=os.path.dirname(os.path.dirname(__file__)),
        capture_output=True,
        text=True,
        check=False,
    )
    assert result.returncode != 0
    assert "CONNECTOR_EGRESS_DENYLIST is required" in result.stderr


@pytest.mark.parametrize(
    "key",
    ["CONNECTOR_MAX_POOLS", "CONNECTOR_MAX_TOTAL_CONNECTIONS"],
)
def test_production_connector_requires_global_pool_limits(key: str) -> None:
    environment = production_connector_environment()
    environment.pop(key)
    result = subprocess.run(
        [sys.executable, "-c", "import app.main"],
        env=environment,
        cwd=os.path.dirname(os.path.dirname(__file__)),
        capture_output=True,
        text=True,
        check=False,
    )
    assert result.returncode != 0
    assert f"{key} is required in production" in result.stderr


@pytest.mark.parametrize(
    ("key", "expected"),
    [
        (
            "CONNECTOR_EGRESS_ALLOWLIST",
            "must contain at least one IP/CIDR target",
        ),
        (
            "CONNECTOR_EGRESS_DENYLIST",
            "must contain at least one control-plane CIDR",
        ),
    ],
)
def test_production_connector_rejects_parsed_empty_egress_rules(
    key: str,
    expected: str,
) -> None:
    environment = production_connector_environment()
    environment[key] = ","
    result = subprocess.run(
        [sys.executable, "-c", "import app.main"],
        env=environment,
        cwd=os.path.dirname(os.path.dirname(__file__)),
        capture_output=True,
        text=True,
        check=False,
    )
    assert result.returncode != 0
    assert expected in result.stderr


def test_egress_dns_requires_every_address_in_authorized_cidr() -> None:
    allowlist = (
        EgressRule(
            port=3306,
            network=ipaddress.ip_network("10.40.0.0/24"),
        ),
    )
    resolved = [
        (2, 1, 6, "", ("10.40.0.10", 3306)),
        (2, 1, 6, "", ("10.99.0.10", 3306)),
    ]
    with (
        patch.object(connector_main, "ENVIRONMENT", "production"),
        patch.object(connector_main, "EGRESS_ALLOWLIST", allowlist),
        patch.object(connector_main, "EGRESS_DENYLIST", ()),
        patch("app.main.socket.getaddrinfo", return_value=resolved),
    ):
        with pytest.raises(EgressDeniedError) as error:
            resolve_egress_target("database.internal", 3306)
    assert str(error.value) == "EGRESS_TARGET_DENIED"


def test_egress_denylist_wins_over_allowlist() -> None:
    address = ipaddress.ip_network("10.40.0.0/24")
    resolved = [(2, 1, 6, "", ("10.40.0.10", 3306))]
    with (
        patch.object(
            connector_main, "ENVIRONMENT", "production",
        ),
        patch.object(
            connector_main, "EGRESS_ALLOWLIST",
            (EgressRule(port=3306, network=address),),
        ),
        patch.object(
            connector_main, "EGRESS_DENYLIST",
            (ipaddress.ip_network("10.40.0.0/28"),),
        ),
        patch("app.main.socket.getaddrinfo", return_value=resolved),
    ):
        with pytest.raises(EgressDeniedError) as error:
            resolve_egress_target("database.internal", 3306)
    assert str(error.value) == "EGRESS_TARGET_DENIED"


@pytest.mark.parametrize(
    "mapped",
    [
        "::ffff:127.0.0.1",
        "::ffff:169.254.169.254",
        "::ffff:10.40.0.10",
    ],
)
def test_egress_rejects_all_ipv4_mapped_ipv6(mapped: str) -> None:
    assert connector_main.forbidden_special_address(
        ipaddress.ip_address(mapped),
    )


def test_open_connection_pins_validated_ip_without_changing_database_semantics() -> None:
    config = ConnectionConfig(
        source_type="MYSQL", host="database.internal", port=3306,
        database="sales", username="reader", password="secret",
    )
    with (
        patch(
            "app.main.resolve_egress_target",
            return_value="10.40.0.10",
        ),
        patch("app.main.pymysql.connect", return_value=object()) as connect,
    ):
        open_connection(config)
    assert connect.call_args.kwargs["host"] == "10.40.0.10"
    assert connect.call_args.kwargs["database"] == "sales"


def test_metadata_sample_rejects_unsafe_table_name_before_connecting() -> None:
    response = TestClient(app).post(
        "/v1/metadata/sample",
        headers={"X-Connector-Token": "local_connector_token_change_me"},
        json={
            "connection": {"source_type": "MYSQL", "host": "none", "port": 3306, "database": "db", "username": "u", "password": "p"},
            "schema_name": "sales",
            "table_name": "orders; DROP TABLE users",
            "max_rows": 3,
        },
    )
    assert response.status_code == 400


def test_metadata_sample_accepts_ten_rows_but_rejects_more() -> None:
    connection = {"source_type": "MYSQL", "host": "none", "port": 3306, "database": "db", "username": "u", "password": "p"}
    assert MetadataSampleRequest(connection=connection, schema_name="sales", table_name="orders", max_rows=10).max_rows == 10
    with pytest.raises(ValidationError):
        MetadataSampleRequest(connection=connection, schema_name="sales", table_name="orders", max_rows=11)


def test_connector_rejects_oversized_http_body_before_model_parsing() -> None:
    response = TestClient(app).post(
        "/v1/query",
        headers={
            "X-Connector-Token": "local_connector_token_change_me",
            "Content-Type": "application/json",
        },
        content=b"x" * (connector_main.HTTP_MAX_REQUEST_BYTES + 1),
    )
    assert response.status_code == 413
    assert response.json()["detail"] == "CONNECTOR_REQUEST_BODY_LIMIT_EXCEEDED"


def test_connector_rejects_chunked_body_without_content_length() -> None:
    async def invoke() -> list[dict]:
        chunks = [
            {
                "type": "http.request",
                "body": b"x" * (connector_main.HTTP_MAX_REQUEST_BYTES // 2),
                "more_body": True,
            },
            {
                "type": "http.request",
                "body": b"x" * (connector_main.HTTP_MAX_REQUEST_BYTES // 2 + 1),
                "more_body": False,
            },
        ]
        sent: list[dict] = []

        async def receive():
            return chunks.pop(0)

        async def send(message):
            sent.append(message)

        await app(
            {
                "type": "http",
                "asgi": {"version": "3.0"},
                "http_version": "1.1",
                "method": "POST",
                "scheme": "http",
                "path": "/v1/query",
                "raw_path": b"/v1/query",
                "query_string": b"",
                "root_path": "",
                "headers": [
                    (b"content-type", b"application/json"),
                    (
                        b"x-connector-token",
                        b"local_connector_token_change_me",
                    ),
                ],
                "client": ("127.0.0.1", 12345),
                "server": ("testserver", 80),
            },
            receive,
            send,
        )
        return sent

    messages = asyncio.run(invoke())
    starts = [
        message for message in messages
        if message["type"] == "http.response.start"
    ]
    assert len(starts) == 1 and starts[0]["status"] == 413


def test_metadata_projection_excludes_lobs_and_large_object_types() -> None:
    class Cursor:
        def execute(self, _sql, _parameters): pass
        def fetchall(self):
            return [
                ("ID", "NUMBER", "NUMBER(18)"),
                ("NOTE", "VARCHAR2", "VARCHAR2(100)"),
                ("BINARY_VALUE", "RAW", "RAW(16)"),
                ("LEGACY_VALUE", "LONG", "LONG"),
                ("XML_VALUE", "XMLTYPE", "XMLTYPE"),
                ("JSON_VALUE", "JSON", "JSON"),
            ]

    request = MetadataSampleRequest(
        connection={
            "source_type": "ORACLE", "host": "database.internal",
            "port": 1521, "database": "FREEPDB1",
            "username": "reader", "password": "secret",
        },
        schema_name="APP", table_name="ORDERS",
        columns=[
            "ID", "NOTE", "BINARY_VALUE", "LEGACY_VALUE",
            "XML_VALUE", "JSON_VALUE",
        ],
    )
    with pytest.raises(connector_main.ResourceBudgetExceeded) as error:
        connector_main.metadata_sample_projection(Cursor(), request)
    assert str(error.value) == "METADATA_SAMPLE_COLUMN_UNSAFE"


def test_metadata_assembly_indexes_constraints_linearly() -> None:
    width = 10_000
    tables = [{
        "catalog_name": "sales",
        "schema_name": "sales",
        "table_name": "wide_table",
        "table_type": "TABLE",
        "source_comment": "",
        "estimated_row_count": 1,
    }]
    columns = [{
        "schema_name": "sales",
        "table_name": "wide_table",
        "column_name": f"c{index}",
        "ordinal_position": index + 1,
        "source_comment": "",
        "native_type": "INT",
        "length": None,
        "numeric_precision": 18,
        "numeric_scale": 0,
        "is_nullable": "NO",
        "default_value": None,
    } for index in range(width)]
    constraints = [{
        "schema_name": "sales",
        "table_name": "wide_table",
        "constraint_name": f"u{index}",
        "constraint_type": "UNIQUE",
        "column_name": f"c{index}",
        "referenced_table_name": None,
        "referenced_column_name": None,
    } for index in range(width)]
    started = connector_main.time.perf_counter()
    result = connector_main.assemble_metadata(
        tables, columns, constraints, [],
    )
    elapsed = connector_main.time.perf_counter() - started
    assert len(result) == 1 and len(result[0]["columns"]) == width
    assert elapsed < 3


@pytest.mark.parametrize("value", [float("nan"), float("inf"), float("-inf")])
def test_stream_rejects_non_finite_numbers_with_stable_code(value: float) -> None:
    with pytest.raises(connector_main.ResourceBudgetExceeded) as error:
        connector_main.bounded_rows(
            [(value,)], 1024, 4096, "QUERY",
        )
    assert str(error.value) == "QUERY_VALUE_UNSUPPORTED"


def test_query_rejects_writes_before_connecting() -> None:
    response = TestClient(app).post(
        "/v1/query",
        headers={"X-Connector-Token": "local_connector_token_change_me"},
        json={
            "connection": {"source_type": "MYSQL", "host": "none", "port": 3306, "database": "db", "username": "u", "password": "p"},
            "sql": "DELETE FROM users",
        },
    )
    assert response.status_code == 400


def test_canonical_types() -> None:
    assert canonical_type("tinyint(1)") == "BOOLEAN"
    assert canonical_type("NUMBER(18,2)") == "NUMBER"
    assert canonical_type("TIMESTAMP(6)") == "DATETIME"


def oracle_config(**overrides) -> ConnectionConfig:
    values = {"source_type": "ORACLE", "host": "oracle", "port": 1521, "database": "FREEPDB1", "username": "reader", "password": "secret"}
    values.update(overrides)
    return ConnectionConfig(**values)


def test_oracle_service_name_sid_and_schema_validation() -> None:
    assert "SERVICE_NAME=FREEPDB1" in oracle_dsn(oracle_config())
    assert "SID=FREE" in oracle_dsn(oracle_config(database="FREE", oracle_connect_mode="SID"))
    assert oracle_config(schemas=["report_reader"]).schemas == ["REPORT_READER"]
    with pytest.raises(ValidationError):
        oracle_config(schemas=["REPORT_READER;DROP TABLE X"])


def test_connection_pool_reuses_connection() -> None:
    connection = object()
    pool = ConnectionPool(oracle_config(max_connections_per_source=1))
    with patch("app.main.open_connection", return_value=connection) as connect:
        first = pool.acquire()
        pool.release(first)
        second = pool.acquire()
        pool.release(second)
    assert first is second
    connect.assert_called_once()
    pool.close()


def test_connection_test_always_uses_one_shot_connection() -> None:
    order: list[str] = []

    class Cursor:
        def execute(self, _sql): order.append("cursor.execute")
        def fetchone(self): return ("8.4",)
        def close(self): order.append("cursor.close")

    class Connection:
        def cursor(self): return Cursor()
        def close(self): order.append("connection.close")

    config = ConnectionConfig(
        source_type="MYSQL", host="database.internal", port=3306,
        database="sales", username="reader", password="secret",
    )
    with patch("app.main.open_connection", return_value=Connection()):
        result = connector_main.test_connection(config)
    assert result["serverVersion"] == "8.4"
    assert order == ["cursor.execute", "cursor.close", "connection.close"]


def test_failed_connection_test_still_closes_one_shot_connection() -> None:
    order: list[str] = []

    class Cursor:
        def execute(self, _sql):
            order.append("cursor.execute")
            raise RuntimeError("driver detail must stay internal")
        def close(self): order.append("cursor.close")

    class Connection:
        def cursor(self): return Cursor()
        def close(self): order.append("connection.close")

    config = ConnectionConfig(
        source_type="MYSQL", host="database.internal", port=3306,
        database="sales", username="reader", password="secret",
    )
    with (
        patch("app.main.open_connection", return_value=Connection()),
        pytest.raises(Exception) as error,
    ):
        connector_main.test_connection(config)
    assert getattr(error.value, "detail", None) == "CONNECTION_TEST_FAILED"
    assert order == ["cursor.execute", "cursor.close", "connection.close"]


def test_global_connection_limit_evicts_only_idle_lru_connection() -> None:
    order: list[str] = []

    class Connection:
        def __init__(self, name: str): self.name = name
        def close(self): order.append(self.name)

    configs = [
        ConnectionConfig(
            source_type="MYSQL", host="database.internal", port=3306,
            database="sales", username="reader", password="secret",
            source_key=f"source-{index}",
        )
        for index in range(2)
    ]
    created = iter((Connection("first"), Connection("second")))
    with (
        patch.object(connector_main, "MAX_TOTAL_CONNECTIONS", 1),
        patch.object(connector_main, "MAX_POOLS", 10),
        patch("app.main.open_connection", side_effect=lambda _config: next(created)),
    ):
        with connector_main.pooled_connection(configs[0]):
            pass
        with connector_main.pooled_connection(configs[1]) as active:
            assert active.name == "second"
            assert order == ["first"]
    with connector_main.POOLS_LOCK:
        pools = list(connector_main.POOLS.values())
        connector_main.POOLS.clear()
    for pool in pools:
        pool.close()
    assert connector_main.GLOBAL_CONNECTIONS == 0


def test_global_connection_limit_never_evicts_active_query() -> None:
    order: list[str] = []

    class Connection:
        def close(self): order.append("active.close")

    first = ConnectionConfig(
        source_type="MYSQL", host="database.internal", port=3306,
        database="sales", username="reader", password="secret",
        source_key="active-source", connect_timeout_seconds=0.01,
    )
    second = first.model_copy(update={"source_key": "waiting-source"})
    with (
        patch.object(connector_main, "MAX_TOTAL_CONNECTIONS", 1),
        patch.object(connector_main, "MAX_POOLS", 10),
        patch("app.main.open_connection", return_value=Connection()) as connect,
    ):
        with connector_main.pooled_connection(first):
            with pytest.raises(TimeoutError, match="global connection limit"):
                with connector_main.pooled_connection(second):
                    pass
            assert order == []
            connect.assert_called_once()
    with connector_main.POOLS_LOCK:
        pools = list(connector_main.POOLS.values())
        connector_main.POOLS.clear()
    for pool in pools:
        pool.close()
    assert order == ["active.close"]
    assert connector_main.GLOBAL_CONNECTIONS == 0


def test_pool_limit_rejects_when_every_pool_is_active() -> None:
    first = ConnectionPool(
        ConnectionConfig(
            source_type="MYSQL", host="database.internal", port=3306,
            database="sales", username="reader", password="secret",
            source_key="active-source",
        ),
    )
    first.registry_references = 1
    with connector_main.POOLS_LOCK:
        connector_main.POOLS["active"] = first
    with patch.object(connector_main, "MAX_POOLS", 1):
        with pytest.raises(TimeoutError, match="pool limit"):
            connector_main.get_pool(
                ConnectionConfig(
                    source_type="MYSQL", host="database.internal", port=3306,
                    database="sales", username="reader", password="secret",
                    source_key="new-source",
                ),
            )
    with connector_main.POOLS_LOCK:
        connector_main.POOLS.clear()
    first.release_registry_reference()
    first.close()


def test_pool_lookup_failure_releases_global_and_tenant_slots() -> None:
    config = ConnectionConfig(
        source_type="MYSQL", host="database.internal", port=3306,
        database="sales", username="reader", password="secret",
        source_key="pool-limit-source", tenant_key="pool-limit-tenant",
    )
    with patch("app.main.get_pool", side_effect=TimeoutError("pool limit")):
        for _ in range(2):
            with pytest.raises(TimeoutError, match="pool limit"):
                with connector_main.pooled_connection(config):
                    pass
            assert connector_main.QUERY_SLOTS.acquire(blocking=False)
            connector_main.QUERY_SLOTS.release()
            tenant_slots = connector_main.get_tenant_slots(config)
            assert tenant_slots.acquire(blocking=False)
            tenant_slots.release()


def test_connection_pool_close_releases_idle_connection() -> None:
    class FakeConnection:
        closed = False
        def close(self): self.closed = True
    connection = FakeConnection()
    pool = ConnectionPool(oracle_config(max_connections_per_source=1))
    with patch("app.main.open_connection", return_value=connection):
        acquired = pool.acquire()
        pool.release(acquired)
    pool.close()
    assert connection.closed is True
    assert pool.created == 0
    with pytest.raises(RuntimeError): pool.acquire()


def test_pool_key_fences_connection_and_query_timeout_changes() -> None:
    slow = oracle_config(
        source_key="source-1",
        connect_timeout_seconds=20,
        query_timeout_seconds=300,
    )
    strict = oracle_config(
        source_key="source-1",
        connect_timeout_seconds=5,
        query_timeout_seconds=15,
    )
    assert connector_main.pool_key(slow) != connector_main.pool_key(strict)


def test_mysql_connection_uses_utf8mb4_and_does_not_reuse_old_transaction_snapshot() -> None:
    config = ConnectionConfig(source_type="MYSQL", host="mysql", port=3306, database="report_source", username="reader", password="secret")
    with patch("app.main.pymysql.connect", return_value=object()) as connect:
        open_connection(config)
    assert connect.call_args.kwargs["charset"] == "utf8mb4"
    assert connect.call_args.kwargs["autocommit"] is True
    assert (
        connect.call_args.kwargs["cursorclass"]
        is connector_main.pymysql.cursors.Cursor
    )


@pytest.mark.parametrize("sql", [
    "WITH selected AS (SELECT id FROM users) DELETE FROM users WHERE id IN (SELECT id FROM selected)",
    "WITH selected AS (SELECT id FROM users) UPDATE users SET name='x'",
    "SELECT * FROM users INTO OUTFILE '/tmp/users'",
    "SELECT SLEEP(10)",
    "SELECT 1 # hidden comment",
    "SELECT 1 -- hidden comment",
    "SELECT 1 /* hidden comment */",
    "SELECT 1; DELETE FROM users",
    "DROP TABLE users",
    "CALL dangerous_procedure()",
    "WITH selected AS (SELECT id FROM users) REPLACE INTO archived_users SELECT * FROM selected",
    "SELECT REPLACE FROM users",
])
def test_read_only_guard_rejects_bypasses(sql: str) -> None:
    with pytest.raises(Exception):
        validate_read_only_sql(sql)


def test_read_only_guard_accepts_select_and_cte() -> None:
    validate_read_only_sql("SELECT 'delete is text' AS note FROM users")
    validate_read_only_sql("WITH selected AS (SELECT id FROM users) SELECT * FROM selected")
    validate_read_only_sql("SELECT REPLACE(name, 'old', 'new') FROM users")
    validate_read_only_sql("SELECT REPLACE (name, 'old', 'new') FROM users")


def test_stream_query_emits_bounded_batches_without_accumulating_rows() -> None:
    class Cursor:
        description = [("id",), ("name",)]
        batches = [[(1, "甲"), (2, "乙")], [(3, "丙")], []]
        def execute(self, _sql, _parameters): pass
        def fetchmany(self, _size): return self.batches.pop(0)
        def close(self): pass

    class Connection:
        def cursor(self, *_args): return Cursor()
        def close(self): pass

    from contextlib import contextmanager
    @contextmanager
    def connection(_config):
        yield Connection()

    request = StreamQueryRequest(
        connection={"source_type": "MYSQL", "host": "db", "port": 3306, "database": "sales", "username": "reader", "password": "secret"},
        sql="SELECT id,name FROM orders", query_id="stream-test", batch_size=2, max_rows=3,
    )
    with patch("app.main.pooled_connection", connection):
        events = [json.loads(line) for line in stream_query_events(request)]
    assert [event["type"] for event in events] == ["schema", "batch", "batch", "complete"]
    assert events[-1]["rowCount"] == 3


def test_stream_query_reports_limit_before_emitting_overflow_batch() -> None:
    class Cursor:
        description = [("id",)]
        batches = [[(1,), (2,)], []]
        def execute(self, _sql, _parameters): pass
        def fetchmany(self, _size): return self.batches.pop(0)
        def close(self): pass

    class Connection:
        def cursor(self, *_args): return Cursor()
        def close(self): pass

    from contextlib import contextmanager
    @contextmanager
    def connection(_config):
        yield Connection()

    request = StreamQueryRequest(
        connection={"source_type": "MYSQL", "host": "db", "port": 3306, "database": "sales", "username": "reader", "password": "secret"},
        sql="SELECT id FROM orders", query_id="stream-limit", batch_size=2, max_rows=1,
    )
    with patch("app.main.pooled_connection", connection):
        events = [json.loads(line) for line in stream_query_events(request)]
    assert [event["type"] for event in events] == ["schema", "error"]
    assert events[-1]["code"] == "QUERY_ROW_LIMIT_EXCEEDED"


class OrderedCursor:
    def __init__(self, order: list[str], batches: list[list[tuple]]):
        self.order = order
        self.batches = list(batches)
        self.description = [("value",)]

    def execute(self, _sql, _parameters):
        self.order.append("execute")

    def fetchmany(self, _size):
        return self.batches.pop(0)

    def close(self):
        self.order.append("cursor.close")


class OrderedConnection:
    def __init__(self, order: list[str], batches: list[list[tuple]]):
        self.order = order
        self.cursor_value = OrderedCursor(order, batches)

    def cursor(self, cursor_class=None):
        assert cursor_class is connector_main.pymysql.cursors.SSCursor
        self.order.append("cursor.sscursor")
        return self.cursor_value

    def close(self):
        self.order.append("connection.close")


def ordered_stream_request(query_id: str, max_rows: int = 10) -> StreamQueryRequest:
    return StreamQueryRequest(
        connection={
            "source_type": "MYSQL", "host": "database.internal",
            "port": 3306, "database": "sales",
            "username": "reader", "password": "secret",
        },
        sql="SELECT value FROM orders",
        query_id=query_id, batch_size=2, max_rows=max_rows,
    )


def test_stream_overflow_closes_socket_before_sscursor_and_never_reuses_pool() -> None:
    order: list[str] = []
    connection = OrderedConnection(order, [[(1,), (2,)], []])
    pool = ConnectionPool(ordered_stream_request("overflow").connection)
    with (
        patch("app.main.get_pool", return_value=pool),
        patch("app.main.open_connection", return_value=connection),
    ):
        events = [
            json.loads(line)
            for line in stream_query_events(
                ordered_stream_request("overflow", max_rows=1),
            )
        ]
    assert [event["type"] for event in events] == ["schema", "error"]
    assert events[-1]["code"] == "QUERY_ROW_LIMIT_EXCEEDED"
    first_socket_close = order.index("connection.close")
    assert first_socket_close < order.index("cursor.close")
    assert pool.created == 0 and pool.idle == []


def test_stream_generator_close_closes_socket_before_sscursor() -> None:
    order: list[str] = []
    connection = OrderedConnection(order, [[(1,)], []])
    pool = ConnectionPool(ordered_stream_request("disconnect").connection)
    with (
        patch("app.main.get_pool", return_value=pool),
        patch("app.main.open_connection", return_value=connection),
    ):
        generator = stream_query_events(ordered_stream_request("disconnect"))
        assert json.loads(next(generator))["type"] == "schema"
        generator.close()
    assert order.index("connection.close") < order.index("cursor.close")
    assert pool.created == 0 and pool.idle == []


def test_stream_success_closes_cursor_then_returns_live_connection_to_pool() -> None:
    order: list[str] = []
    connection = OrderedConnection(order, [[(1,)], []])
    pool = ConnectionPool(ordered_stream_request("success").connection)
    with (
        patch("app.main.get_pool", return_value=pool),
        patch("app.main.open_connection", return_value=connection),
    ):
        events = [
            json.loads(line)
            for line in stream_query_events(ordered_stream_request("success"))
        ]
    assert [event["type"] for event in events] == [
        "schema", "batch", "complete",
    ]
    assert "cursor.close" in order
    assert "connection.close" not in order
    assert pool.created == 1 and pool.idle == [connection]
    pool.close()


def test_query_response_budget_closes_socket_before_sscursor() -> None:
    order: list[str] = []
    connection = OrderedConnection(
        order,
        [[("x" * 256,)], []],
    )
    pool = ConnectionPool(ordered_stream_request("query-budget").connection)
    request = connector_main.QueryRequest(
        connection=ordered_stream_request("query-budget").connection,
        sql="SELECT value FROM orders",
        query_id="query-budget", max_rows=10,
    )
    with (
        patch("app.main.get_pool", return_value=pool),
        patch("app.main.open_connection", return_value=connection),
        patch.object(connector_main, "JSON_MAX_RESPONSE_BYTES", 192),
    ):
        with pytest.raises(Exception) as error:
            query(request)
    assert getattr(error.value, "status_code", None) == 413
    assert order.index("connection.close") < order.index("cursor.close")
    assert pool.created == 0 and pool.idle == []


def test_query_row_overflow_closes_socket_before_sscursor() -> None:
    order: list[str] = []
    connection = OrderedConnection(order, [[(1,), (2,)], []])
    pool = ConnectionPool(ordered_stream_request("query-rows").connection)
    request = connector_main.QueryRequest(
        connection=ordered_stream_request("query-rows").connection,
        sql="SELECT value FROM orders",
        query_id="query-rows", max_rows=1,
    )
    with (
        patch("app.main.get_pool", return_value=pool),
        patch("app.main.open_connection", return_value=connection),
    ):
        with pytest.raises(Exception) as error:
            query(request)
    assert getattr(error.value, "status_code", None) == 413
    assert getattr(error.value, "detail", None) == "QUERY_ROW_LIMIT_EXCEEDED"
    assert order.index("connection.close") < order.index("cursor.close")
    assert pool.created == 0 and pool.idle == []


def test_metadata_sync_budget_uses_sscursor_and_closes_socket_first() -> None:
    order: list[str] = []
    connection = OrderedConnection(order, [[("one",), ("two",)], []])
    config = ordered_stream_request("metadata-sync").connection
    pool = ConnectionPool(config)
    with (
        patch("app.main.get_pool", return_value=pool),
        patch("app.main.open_connection", return_value=connection),
        patch.object(connector_main, "METADATA_SYNC_MAX_ROWS", 1),
    ):
        with pytest.raises(Exception) as error:
            connector_main.sync_metadata(config)
    assert getattr(error.value, "status_code", None) == 413
    assert getattr(error.value, "detail", None) == (
        "METADATA_SYNC_ROW_LIMIT_EXCEEDED"
    )
    assert "cursor.sscursor" in order
    assert order.index("connection.close") < order.index("cursor.close")
    assert pool.created == 0 and pool.idle == []
