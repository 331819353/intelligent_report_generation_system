"""隔离数据库驱动、元数据采集和只读查询能力的连接器服务。"""

import os
import re
import time
import hashlib
import json
import threading
import uuid
from contextlib import contextmanager, closing
from datetime import datetime, timezone
from typing import Any, Literal

import oracledb
import pymysql
from fastapi import Depends, FastAPI, Header, HTTPException
from pydantic import BaseModel, Field, SecretStr, field_validator

app = FastAPI(title="Intelligent Report Connector Service", version="0.1.0")
INTERNAL_TOKEN = os.getenv("CONNECTOR_INTERNAL_TOKEN", "local_connector_token_change_me")
MAX_CONNECTIONS_PER_SOURCE = int(os.getenv("CONNECTOR_MAX_CONNECTIONS_PER_SOURCE", "5"))
MAX_CONCURRENT_QUERIES = int(os.getenv("CONNECTOR_MAX_CONCURRENT_QUERIES", "10"))
POOL_IDLE_TTL_SECONDS = int(os.getenv("CONNECTOR_POOL_IDLE_TTL_SECONDS", "300"))
QUERY_SLOTS = threading.BoundedSemaphore(MAX_CONCURRENT_QUERIES)
TENANT_SLOTS: dict[str, threading.BoundedSemaphore] = {}
TENANT_SLOTS_LOCK = threading.Lock()


class ConnectionConfig(BaseModel):
    """描述单个数据源的连接参数、超时和并发上限。"""

    source_type: Literal["MYSQL", "ORACLE"]
    host: str = Field(min_length=1, max_length=255)
    port: int = Field(gt=0, le=65535)
    database: str = Field(min_length=1, max_length=255)
    username: str = Field(min_length=1, max_length=255)
    password: SecretStr
    connect_timeout_seconds: float = Field(default=5, gt=0, le=30)
    query_timeout_seconds: float = Field(default=15, gt=0, le=300)
    oracle_connect_mode: Literal["SERVICE_NAME", "SID"] = "SERVICE_NAME"
    schemas: list[str] = Field(default_factory=list, max_length=20)
    tenant_key: str = Field(default="default", min_length=1, max_length=100)
    source_key: str = Field(default="default", min_length=1, max_length=100)
    max_connections_per_source: int = Field(default=5, gt=0, le=100)
    max_concurrent_queries: int = Field(default=10, gt=0, le=500)

    @field_validator("schemas")
    @classmethod
    def validate_schemas(cls, values: list[str]) -> list[str]:
        """规范化 Oracle 模式名，并拒绝可能形成标识符注入的值。"""
        normalized = []
        for value in values:
            value = value.strip().upper()
            if not re.fullmatch(r"[A-Z][A-Z0-9_$#]{0,127}", value):
                raise ValueError("invalid Oracle schema name")
            if value not in normalized: normalized.append(value)
        return normalized


class ConnectionPool:
    """按数据源隔离的轻量连接池，限制连接数并在关闭时唤醒等待线程。"""

    def __init__(self, config: ConnectionConfig):
        """根据数据源配置初始化有界连接池。"""
        self.config, self.idle, self.created = config, [], 0
        self.maximum = min(config.max_connections_per_source, MAX_CONNECTIONS_PER_SOURCE)
        self.condition = threading.Condition()
        self.in_use, self.closed, self.last_used = 0, False, time.monotonic()

    def acquire(self):
        """复用空闲连接或在容量内创建连接，超时后主动失败。"""
        deadline = time.monotonic() + self.config.connect_timeout_seconds
        with self.condition:
            if self.closed: raise RuntimeError("connection pool is closed")
            while True:
                if self.closed: raise RuntimeError("connection pool is closed")
                if self.idle:
                    self.in_use += 1
                    return self.idle.pop()
                if self.created < self.maximum:
                    self.created += 1
                    self.in_use += 1
                    break
                remaining = deadline - time.monotonic()
                if remaining <= 0: raise TimeoutError("connection pool exhausted")
                self.condition.wait(remaining)
        try: return open_connection(self.config)
        except Exception:
            with self.condition:
                self.created -= 1
                self.in_use -= 1
                self.condition.notify()
            raise

    def release(self, connection, broken: bool = False) -> None:
        """归还健康连接；损坏或已关闭池中的连接会被直接销毁。"""
        with self.condition:
            self.in_use -= 1
            self.last_used = time.monotonic()
            if broken or self.closed:
                try: connection.close()
                finally: self.created -= 1
            else: self.idle.append(connection)
            self.condition.notify()

    def close(self) -> None:
        """关闭全部空闲连接并唤醒正在等待的线程。"""
        with self.condition:
            self.closed = True
            for connection in self.idle:
                try: connection.close()
                except Exception: pass
            self.created -= len(self.idle)
            self.idle.clear()
            self.condition.notify_all()


POOLS: dict[str, ConnectionPool] = {}
POOLS_LOCK = threading.Lock()
ACTIVE_QUERIES: dict[str, Any] = {}
ACTIVE_QUERIES_LOCK = threading.Lock()


def pool_key(config: ConnectionConfig) -> str:
    """连接池键包含认证和容量参数；只保存摘要，避免明文密码进入全局字典。"""
    raw = "\x1f".join((config.source_key, config.source_type, config.host, str(config.port), config.database,
        config.username, config.password.get_secret_value(), config.oracle_connect_mode,
        str(config.max_connections_per_source)))
    return hashlib.sha256(raw.encode()).hexdigest()


def get_pool(config: ConnectionConfig) -> ConnectionPool:
    """获取数据源专属连接池，并顺便清理过期空闲池。"""
    key = pool_key(config)
    with POOLS_LOCK:
        evict_idle_pools_locked()
        if key not in POOLS: POOLS[key] = ConnectionPool(config)
        return POOLS[key]


def evict_idle_pools_locked() -> None:
    """调用方必须持有 POOLS_LOCK，空闲超时的连接池会被完整关闭。"""
    now = time.monotonic()
    expired = [key for key, pool in POOLS.items() if pool.in_use == 0 and now - pool.last_used >= POOL_IDLE_TTL_SECONDS]
    for key in expired: POOLS.pop(key).close()


def close_pool(config: ConnectionConfig) -> bool:
    """从全局注册表移除并关闭指定数据源连接池。"""
    with POOLS_LOCK: pool = POOLS.pop(pool_key(config), None)
    if pool: pool.close()
    return pool is not None


def get_tenant_slots(config: ConnectionConfig) -> threading.BoundedSemaphore:
    """按租户和配置上限复用查询并发信号量。"""
    limit = min(config.max_concurrent_queries, MAX_CONCURRENT_QUERIES)
    key = f"{config.tenant_key}:{limit}"
    with TENANT_SLOTS_LOCK:
        if key not in TENANT_SLOTS: TENANT_SLOTS[key] = threading.BoundedSemaphore(limit)
        return TENANT_SLOTS[key]


@contextmanager
def pooled_connection(config: ConnectionConfig):
    """依次获取全局、租户和数据源连接配额，并保证逆序释放。"""
    if not QUERY_SLOTS.acquire(timeout=config.connect_timeout_seconds):
        raise TimeoutError("connector concurrency limit exceeded")
    tenant_slots = get_tenant_slots(config)
    if not tenant_slots.acquire(timeout=config.connect_timeout_seconds):
        QUERY_SLOTS.release()
        raise TimeoutError("tenant query concurrency limit exceeded")
    pool, connection, broken = get_pool(config), None, False
    try:
        connection = pool.acquire()
        yield connection
    except Exception:
        broken = True
        raise
    finally:
        if connection is not None: pool.release(connection, broken)
        tenant_slots.release()
        QUERY_SLOTS.release()


class QueryRequest(BaseModel):
    """定义受控只读查询及其行数、参数和取消标识。"""

    connection: ConnectionConfig
    sql: str = Field(min_length=1, max_length=100_000)
    parameters: list[Any] = Field(default_factory=list, max_length=1000)
    max_rows: int = Field(default=10_000, gt=0, le=100_000)
    query_id: str = Field(default_factory=lambda: str(uuid.uuid4()), min_length=1, max_length=100)


class CancelRequest(BaseModel):
    """标识需要中断的在途查询。"""

    query_id: str = Field(min_length=1, max_length=100)


def canonical_type(native_type: str) -> str:
    """将 MySQL 和 Oracle 原生类型归一为平台规范类型。"""
    value = native_type.upper()
    if "BOOL" in value or value == "TINYINT(1)": return "BOOLEAN"
    if any(item in value for item in ("INT", "NUMBER")): return "NUMBER"
    if any(item in value for item in ("DECIMAL", "NUMERIC", "FLOAT", "DOUBLE", "REAL")): return "DECIMAL"
    if "TIMESTAMP" in value or "DATETIME" in value: return "DATETIME"
    if value == "DATE": return "DATE"
    if "TIME" in value: return "TIME"
    if any(item in value for item in ("BLOB", "BINARY", "RAW")): return "BINARY"
    return "TEXT"


def rows_as_dicts(cursor) -> list[dict[str, Any]]:
    """将游标结果按小写列名转换为字典列表。"""
    names = [item[0].lower() for item in cursor.description or []]
    return [dict(zip(names, row)) for row in cursor.fetchall()]


def fetch(cursor, sql: str, parameters=None) -> list[dict[str, Any]]:
    """执行内部元数据查询并返回字典化结果。"""
    cursor.execute(sql, parameters or [])
    return rows_as_dicts(cursor)


def collect_mysql(cursor) -> list[dict[str, Any]]:
    """从 information_schema 采集当前 MySQL 数据库的技术元数据。"""
    tables = fetch(cursor, """SELECT table_schema catalog_name, table_schema schema_name, table_name,
        table_type, COALESCE(table_comment,'') source_comment, table_rows estimated_row_count
        FROM information_schema.tables WHERE table_schema=DATABASE() ORDER BY table_name""")
    columns = fetch(cursor, """SELECT table_schema schema_name, table_name, column_name, ordinal_position,
        COALESCE(column_comment,'') source_comment, column_type native_type, data_type,
        character_maximum_length length, numeric_precision, numeric_scale,
        is_nullable, column_default default_value, column_key
        FROM information_schema.columns WHERE table_schema=DATABASE() ORDER BY table_name,ordinal_position""")
    constraints = fetch(cursor, """SELECT tc.table_schema schema_name,tc.table_name,tc.constraint_name,tc.constraint_type,
        kcu.column_name,kcu.ordinal_position,kcu.referenced_table_name,kcu.referenced_column_name
        FROM information_schema.table_constraints tc JOIN information_schema.key_column_usage kcu
        ON tc.constraint_schema=kcu.constraint_schema AND tc.table_name=kcu.table_name AND tc.constraint_name=kcu.constraint_name
        WHERE tc.constraint_schema=DATABASE() ORDER BY tc.table_name,tc.constraint_name,kcu.ordinal_position""")
    indexes = fetch(cursor, """SELECT table_schema schema_name,table_name,index_name,non_unique,column_name,seq_in_index
        FROM information_schema.statistics WHERE table_schema=DATABASE()
        ORDER BY table_name,index_name,seq_in_index""")
    return assemble_metadata(tables, columns, constraints, indexes)


def collect_oracle(cursor, schemas: list[str]) -> list[dict[str, Any]]:
    """从 Oracle 数据字典采集白名单模式下的技术元数据。"""
    placeholders = ",".join(f":{index + 1}" for index in range(len(schemas)))
    tables = fetch(cursor, f"""SELECT o.owner catalog_name, o.owner schema_name, o.object_name table_name,
        o.object_type table_type, NVL(c.comments,'') source_comment, t.num_rows estimated_row_count
        FROM all_objects o LEFT JOIN all_tab_comments c ON c.owner=o.owner AND c.table_name=o.object_name
        LEFT JOIN all_tables t ON t.owner=o.owner AND t.table_name=o.object_name
        WHERE o.object_type IN ('TABLE','VIEW') AND o.owner IN ({placeholders}) ORDER BY o.owner,o.object_name""", schemas)
    columns = fetch(cursor, f"""SELECT c.owner schema_name,c.table_name,c.column_name,c.column_id ordinal_position,
        NVL(cc.comments,'') source_comment,c.data_type native_type,c.data_type,
        c.char_length length,c.data_precision numeric_precision,c.data_scale numeric_scale,
        c.nullable is_nullable,c.data_default default_value,CAST(NULL AS VARCHAR2(3)) column_key
        FROM all_tab_columns c LEFT JOIN all_col_comments cc
        ON cc.owner=c.owner AND cc.table_name=c.table_name AND cc.column_name=c.column_name
        WHERE c.owner IN ({placeholders}) ORDER BY c.owner,c.table_name,c.column_id""", schemas)
    constraints = fetch(cursor, f"""SELECT c.owner schema_name,c.table_name,c.constraint_name,
        DECODE(c.constraint_type,'P','PRIMARY KEY','U','UNIQUE','R','FOREIGN KEY','C','CHECK',c.constraint_type) constraint_type,
        cc.column_name,cc.position ordinal_position,rc.table_name referenced_table_name,rcc.column_name referenced_column_name
        FROM all_constraints c JOIN all_cons_columns cc ON cc.owner=c.owner AND cc.constraint_name=c.constraint_name
        LEFT JOIN all_constraints rc ON rc.owner=c.r_owner AND rc.constraint_name=c.r_constraint_name
        LEFT JOIN all_cons_columns rcc ON rcc.owner=rc.owner AND rcc.constraint_name=rc.constraint_name AND rcc.position=cc.position
        WHERE c.constraint_type IN ('P','U','R','C') AND c.owner IN ({placeholders})
        ORDER BY c.owner,c.table_name,c.constraint_name,cc.position""", schemas)
    indexes = fetch(cursor, f"""SELECT i.table_owner schema_name,i.table_name,i.index_name,DECODE(i.uniqueness,'UNIQUE',0,1) non_unique,
        c.column_name,c.column_position seq_in_index FROM all_indexes i JOIN all_ind_columns c
        ON c.index_owner=i.owner AND c.index_name=i.index_name
        WHERE i.table_owner IN ({placeholders}) ORDER BY i.table_owner,i.table_name,i.index_name,c.column_position""", schemas)
    return assemble_metadata(tables, columns, constraints, indexes)


def assemble_metadata(tables, columns, constraints, indexes) -> list[dict[str, Any]]:
    """把不同数据库的扁平查询结果组装为统一的表级元数据树。"""
    by_table = {(str(t["schema_name"]), str(t["table_name"])): {**t, "columns": [], "constraints": [], "indexes": []} for t in tables}
    grouped_constraints: dict[tuple[str, str, str], dict[str, Any]] = {}
    for item in constraints:
        key = (str(item.get("schema_name", "")), str(item["table_name"]), str(item["constraint_name"]))
        value = grouped_constraints.setdefault(key, {"name": item["constraint_name"], "type": item["constraint_type"], "columns": [], "referencedTable": item.get("referenced_table_name"), "referencedColumns": []})
        value["columns"].append(item["column_name"])
        if item.get("referenced_column_name"): value["referencedColumns"].append(item["referenced_column_name"])
    grouped_indexes: dict[tuple[str, str, str], dict[str, Any]] = {}
    for item in indexes:
        key = (str(item.get("schema_name", "")), str(item["table_name"]), str(item["index_name"]))
        value = grouped_indexes.setdefault(key, {"name": item["index_name"], "unique": int(item["non_unique"]) == 0, "columns": []})
        value["columns"].append(item["column_name"])
    for item in columns:
        table_key = (str(item.get("schema_name", "")), str(item["table_name"]))
        table = by_table.get(table_key)
        if table is None: continue
        keys = [c for (schema, name, _), c in grouped_constraints.items() if (schema, name) == table_key and item["column_name"] in c["columns"]]
        table["columns"].append({
            "name": item["column_name"], "ordinalPosition": int(item["ordinal_position"]),
            "sourceComment": item.get("source_comment") or "", "nativeType": item["native_type"],
            "canonicalType": canonical_type(str(item["native_type"])), "length": item.get("length"),
            "precision": item.get("numeric_precision"), "scale": item.get("numeric_scale"),
            "nullable": str(item["is_nullable"]).upper() in ("YES", "Y"),
            "defaultValue": str(item["default_value"]).strip() if item.get("default_value") is not None else None,
            "primaryKey": any(c["type"] == "PRIMARY KEY" for c in keys),
            "foreignKey": any(c["type"] == "FOREIGN KEY" for c in keys),
            "unique": any(c["type"] in ("PRIMARY KEY", "UNIQUE") for c in keys),
        })
    for (schema, name, _), item in grouped_constraints.items():
        if (schema, name) in by_table: by_table[(schema, name)]["constraints"].append(item)
    for (schema, name, _), item in grouped_indexes.items():
        if (schema, name) in by_table: by_table[(schema, name)]["indexes"].append(item)
    result = []
    for table in by_table.values():
        table["catalogName"] = str(table.pop("catalog_name") or "")
        table["schemaName"] = str(table.pop("schema_name"))
        table["name"] = str(table.pop("table_name"))
        table["type"] = str(table.pop("table_type"))
        table["sourceComment"] = str(table.pop("source_comment") or "")
        estimated = table.pop("estimated_row_count")
        table["estimatedRowCount"] = int(estimated) if estimated is not None else None
        table["primaryKeyColumns"] = next((c["columns"] for c in table["constraints"] if c["type"] == "PRIMARY KEY"), [])
        result.append(table)
    return result


def authorize(x_connector_token: str = Header(default="")) -> None:
    """校验仅供 Go 主服务使用的内部访问令牌。"""
    if not INTERNAL_TOKEN or x_connector_token != INTERNAL_TOKEN:
        raise HTTPException(status_code=401, detail="invalid connector token")


def oracle_dsn(config: ConnectionConfig) -> str:
    """按 SID 或服务名模式构造 Oracle DSN。"""
    if config.oracle_connect_mode == "SID":
        return oracledb.makedsn(config.host, config.port, sid=config.database)
    return oracledb.makedsn(config.host, config.port, service_name=config.database)


def open_connection(config: ConnectionConfig):
    """创建配置了连接和查询超时的 MySQL 或 Oracle 物理连接。"""
    password = config.password.get_secret_value()
    if config.source_type == "MYSQL":
        return pymysql.connect(
            host=config.host, port=config.port, user=config.username, password=password,
            database=config.database, connect_timeout=int(config.connect_timeout_seconds),
            read_timeout=int(config.query_timeout_seconds), write_timeout=int(config.query_timeout_seconds),
            charset="utf8mb4", cursorclass=pymysql.cursors.Cursor,
        )
    connection = oracledb.connect(
        user=config.username, password=password,
        dsn=oracle_dsn(config),
        tcp_connect_timeout=config.connect_timeout_seconds,
    )
    connection.call_timeout = int(config.query_timeout_seconds * 1000)
    return connection


def validate_read_only_sql(sql: str) -> None:
    """以失败关闭策略拒绝多语句、注释和任何写入或管理操作。"""
    normalized = sql.strip()
    if ";" in normalized or "--" in normalized or "/*" in normalized or "#" in normalized:
        raise HTTPException(status_code=400, detail="comments and multiple statements are forbidden")
    tokens = sql_tokens(normalized)
    if not tokens or tokens[0] not in ("SELECT", "WITH"):
        raise HTTPException(status_code=400, detail="only read-only SELECT queries are allowed")
    forbidden = {
        "INSERT", "UPDATE", "DELETE", "MERGE", "REPLACE", "UPSERT", "CREATE", "ALTER", "DROP",
        "TRUNCATE", "RENAME", "GRANT", "REVOKE", "CALL", "EXEC", "EXECUTE", "BEGIN", "COMMIT",
        "ROLLBACK", "SAVEPOINT", "LOCK", "UNLOCK", "OUTFILE", "DUMPFILE", "LOAD_FILE",
        "GET_LOCK", "RELEASE_LOCK", "SLEEP", "BENCHMARK",
    }
    rejected = forbidden.intersection(tokens)
    if rejected:
        raise HTTPException(status_code=400, detail="query contains a forbidden operation")


def sql_tokens(sql: str) -> list[str]:
    """提取引号外词元；这是失败关闭防线，数据源账号本身仍必须只读。"""
    tokens, current, quote, index = [], [], None, 0
    while index < len(sql):
        char = sql[index]
        if quote:
            if char == quote:
                if index + 1 < len(sql) and sql[index + 1] == quote:
                    index += 2
                    continue
                quote = None
            elif char == "\\" and index + 1 < len(sql):
                index += 2
                continue
            index += 1
            continue
        if char in ("'", '"', "`"):
            if current: tokens.append("".join(current).upper()); current = []
            quote = char
        elif char.isalnum() or char in ("_", "$"):
            current.append(char)
        elif current:
            tokens.append("".join(current).upper()); current = []
        index += 1
    if quote: raise HTTPException(status_code=400, detail="unterminated quoted value")
    if current: tokens.append("".join(current).upper())
    return tokens


@app.get("/health/live")
def live() -> dict[str, str]:
    """返回不依赖外部数据库的进程存活状态。"""
    return {"status": "live"}


@app.post("/v1/connections/test", dependencies=[Depends(authorize)])
def test_connection(config: ConnectionConfig) -> dict[str, Any]:
    """执行轻量版本查询，验证连接并报告往返延迟。"""
    started = time.perf_counter()
    try:
        with pooled_connection(config) as connection, closing(connection.cursor()) as cursor:
            cursor.execute("SELECT VERSION()" if config.source_type == "MYSQL" else "SELECT BANNER FROM V$VERSION WHERE ROWNUM = 1")
            version = str(cursor.fetchone()[0])
    except Exception as exc:
        raise HTTPException(status_code=502, detail=f"connection test failed: {type(exc).__name__}") from exc
    return {"serverVersion": version, "latencyMs": int((time.perf_counter() - started) * 1000)}


@app.post("/v1/connections/close", dependencies=[Depends(authorize)])
def close_connection_pool(config: ConnectionConfig) -> dict[str, bool]:
    """显式释放指定数据源的池化连接。"""
    return {"closed": close_pool(config)}


@app.post("/v1/query/cancel", dependencies=[Depends(authorize)])
def cancel_query(request: CancelRequest) -> dict[str, bool]:
    """按查询标识中断在途执行；MySQL 通过关闭连接实现取消。"""
    with ACTIVE_QUERIES_LOCK: active = ACTIVE_QUERIES.get(request.query_id)
    if active is None: return {"cancelled": False}
    source_type, connection = active
    try:
        if source_type == "ORACLE": connection.cancel()
        else: connection.close()
    except Exception: pass
    return {"cancelled": True}


@app.post("/v1/metadata/sync", dependencies=[Depends(authorize)])
def sync_metadata(config: ConnectionConfig) -> dict[str, Any]:
    """采集元数据并返回带时间水位和稳定哈希的完整快照。"""
    try:
        with pooled_connection(config) as connection, closing(connection.cursor()) as cursor:
            assets = collect_mysql(cursor) if config.source_type == "MYSQL" else collect_oracle(cursor, config.schemas or [config.username.upper()])
    except Exception as exc:
        raise HTTPException(status_code=502, detail=f"metadata sync failed: {type(exc).__name__}") from exc
    snapshot_json = json.dumps(assets, sort_keys=True, separators=(",", ":"), default=str)
    return {
        "assets": len(assets),
        "watermark": datetime.now(timezone.utc).isoformat(),
        "snapshotHash": hashlib.sha256(snapshot_json.encode()).hexdigest(),
        "tables": assets,
    }


@app.post("/v1/query", dependencies=[Depends(authorize)])
def query(request: QueryRequest) -> dict[str, Any]:
    """执行参数化只读查询，并严格限制返回行数和并发资源。"""
    validate_read_only_sql(request.sql)
    started = time.perf_counter()
    try:
        with pooled_connection(request.connection) as connection, closing(connection.cursor()) as cursor:
            with ACTIVE_QUERIES_LOCK:
                if request.query_id in ACTIVE_QUERIES: raise HTTPException(status_code=409, detail="query id is already active")
                ACTIVE_QUERIES[request.query_id] = (request.connection.source_type, connection)
            try:
                cursor.execute(request.sql, request.parameters)
                columns = [item[0] for item in cursor.description or []]
                # 多取一行用于检测超限，避免把截断结果误报为完整结果。
                rows = cursor.fetchmany(request.max_rows + 1)
            finally:
                with ACTIVE_QUERIES_LOCK: ACTIVE_QUERIES.pop(request.query_id, None)
    except HTTPException:
        raise
    except Exception as exc:
        raise HTTPException(status_code=502, detail=f"query failed: {type(exc).__name__}") from exc
    if len(rows) > request.max_rows:
        raise HTTPException(status_code=413, detail="query row limit exceeded")
    return {"columns": columns, "rows": rows, "rowCount": len(rows), "durationMs": int((time.perf_counter() - started) * 1000)}
