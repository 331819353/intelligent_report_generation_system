from fastapi.testclient import TestClient
from unittest.mock import patch
import pytest
from pydantic import ValidationError

from app.main import ConnectionConfig, ConnectionPool, app, canonical_type, oracle_dsn, validate_read_only_sql


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
    assert first is second
    connect.assert_called_once()


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
])
def test_read_only_guard_rejects_bypasses(sql: str) -> None:
    with pytest.raises(Exception):
        validate_read_only_sql(sql)


def test_read_only_guard_accepts_select_and_cte() -> None:
    validate_read_only_sql("SELECT 'delete is text' AS note FROM users")
    validate_read_only_sql("WITH selected AS (SELECT id FROM users) SELECT * FROM selected")
