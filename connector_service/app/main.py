"""隔离数据库驱动、元数据采集和只读查询能力的连接器服务。"""

import os
import re
import time
import hashlib
import ipaddress
import json
import math
import socket
import threading
import uuid
from contextlib import contextmanager, closing
from dataclasses import dataclass
from datetime import date, datetime, time as datetime_time, timezone
from decimal import Decimal
from typing import Any, Iterator, Literal

import oracledb
import pymysql
from fastapi import Depends, FastAPI, Header, HTTPException
from fastapi.responses import JSONResponse, Response, StreamingResponse
from pydantic import BaseModel, Field, SecretStr, field_validator

ENVIRONMENT = os.getenv("APP_ENV", "development").lower()
CONFIGURED_INTERNAL_TOKEN = os.getenv("CONNECTOR_INTERNAL_TOKEN", "")
if ENVIRONMENT == "production" and not CONFIGURED_INTERNAL_TOKEN:
    raise RuntimeError("general connector token is required in production")
INTERNAL_TOKEN = CONFIGURED_INTERNAL_TOKEN or "local_connector_token_change_me"
if ENVIRONMENT == "production" and INTERNAL_TOKEN == "local_connector_token_change_me":
    raise RuntimeError("development connector token is forbidden in production")
CONFIGURED_CONNECTION_TEST_TOKEN = os.getenv("CONNECTOR_CONNECTION_TEST_TOKEN", "")
if ENVIRONMENT == "production" and not CONFIGURED_CONNECTION_TEST_TOKEN:
    raise RuntimeError("connection-test connector token is required in production")
CONNECTION_TEST_TOKEN = (
    CONFIGURED_CONNECTION_TEST_TOKEN
    or "local_connector_connection_test_token_change_me"
)
if (
    ENVIRONMENT == "production"
    and CONNECTION_TEST_TOKEN == INTERNAL_TOKEN
):
    raise RuntimeError("connection-test connector token must be distinct in production")


def configured_byte_limit(
    name: str,
    default: int,
    minimum: int,
    maximum: int,
) -> int:
    """读取字节上限，并在生产环境拒绝依赖开发默认值。"""
    raw = os.getenv(name, "")
    if ENVIRONMENT == "production" and not raw.strip():
        raise RuntimeError(f"{name} is required in production")
    try:
        value = int(raw) if raw.strip() else default
    except ValueError as exc:
        raise RuntimeError(f"{name} must be an integer") from exc
    if value < minimum or value > maximum:
        raise RuntimeError(f"{name} is outside the supported range")
    return value


def configured_count_limit(
    name: str,
    default: int,
    minimum: int,
    maximum: int,
    require_in_production: bool = True,
) -> int:
    """读取行/对象数量上限，并可要求 production 显式配置。"""
    raw = os.getenv(name, "")
    if (
        require_in_production
        and ENVIRONMENT == "production"
        and not raw.strip()
    ):
        raise RuntimeError(f"{name} is required in production")
    try:
        value = int(raw) if raw.strip() else default
    except ValueError as exc:
        raise RuntimeError(f"{name} must be an integer") from exc
    if value < minimum or value > maximum:
        raise RuntimeError(f"{name} is outside the supported range")
    return value


MAX_CONNECTIONS_PER_SOURCE = configured_count_limit(
    "CONNECTOR_MAX_CONNECTIONS_PER_SOURCE", 5, 1, 100, False,
)
MAX_CONCURRENT_QUERIES = configured_count_limit(
    "CONNECTOR_MAX_CONCURRENT_QUERIES", 10, 1, 500, False,
)
MAX_POOLS = configured_count_limit(
    "CONNECTOR_MAX_POOLS", 1000, 1, 10_000,
)
MAX_TOTAL_CONNECTIONS = configured_count_limit(
    "CONNECTOR_MAX_TOTAL_CONNECTIONS", 100, 1, 10_000,
)
POOL_IDLE_TTL_SECONDS = configured_count_limit(
    "CONNECTOR_POOL_IDLE_TTL_SECONDS", 300, 1, 86_400, False,
)


HTTP_MAX_REQUEST_BYTES = configured_byte_limit(
    "CONNECTOR_HTTP_MAX_REQUEST_BYTES", 1 << 20, 4096, 16 << 20,
)
JSON_MAX_RESPONSE_BYTES = configured_byte_limit(
    "CONNECTOR_JSON_MAX_RESPONSE_BYTES", 64 << 20, 64 << 10, 256 << 20,
)
METADATA_SYNC_MAX_ROWS = configured_count_limit(
    "CONNECTOR_METADATA_SYNC_MAX_ROWS", 200_000, 1000, 2_000_000,
)
METADATA_SAMPLE_MAX_CELL_BYTES = configured_byte_limit(
    "CONNECTOR_METADATA_SAMPLE_MAX_CELL_BYTES", 16 << 10, 256, 1 << 20,
)
METADATA_SAMPLE_MAX_ROW_BYTES = configured_byte_limit(
    "CONNECTOR_METADATA_SAMPLE_MAX_ROW_BYTES", 64 << 10, 1024, 4 << 20,
)
METADATA_SAMPLE_MAX_RESPONSE_BYTES = configured_byte_limit(
    "CONNECTOR_METADATA_SAMPLE_MAX_RESPONSE_BYTES", 512 << 10, 4096, 8 << 20,
)
STREAM_MAX_CELL_BYTES = configured_byte_limit(
    "CONNECTOR_STREAM_MAX_CELL_BYTES", 1 << 20, 1024, 16 << 20,
)
STREAM_MAX_ROW_BYTES = configured_byte_limit(
    "CONNECTOR_STREAM_MAX_ROW_BYTES", 4 << 20, 4096, 64 << 20,
)
STREAM_MAX_BYTES = configured_byte_limit(
    "CONNECTOR_STREAM_MAX_BYTES", 1 << 30, 1 << 20, 16 << 30,
)
if METADATA_SAMPLE_MAX_CELL_BYTES > METADATA_SAMPLE_MAX_ROW_BYTES:
    raise RuntimeError(
        "CONNECTOR_METADATA_SAMPLE_MAX_CELL_BYTES must not exceed the row limit",
    )
if METADATA_SAMPLE_MAX_ROW_BYTES > METADATA_SAMPLE_MAX_RESPONSE_BYTES:
    raise RuntimeError(
        "CONNECTOR_METADATA_SAMPLE_MAX_ROW_BYTES must not exceed the response limit",
    )
if STREAM_MAX_CELL_BYTES > STREAM_MAX_ROW_BYTES:
    raise RuntimeError(
        "CONNECTOR_STREAM_MAX_CELL_BYTES must not exceed the row limit",
    )
if STREAM_MAX_ROW_BYTES > STREAM_MAX_BYTES:
    raise RuntimeError(
        "CONNECTOR_STREAM_MAX_ROW_BYTES must not exceed the stream limit",
    )

MAX_METADATA_SAMPLE_COLUMNS = 256
STREAM_EVENT_MAX_BYTES = min(8 << 20, STREAM_MAX_BYTES)
STREAM_TERMINAL_RESERVE_BYTES = 512


def valid_hostname(value: str) -> bool:
    """仅接受普通 DNS 名；URI、用户信息、路径和模糊尾点一律拒绝。"""
    if (
        not value
        or len(value) > 253
        or value != value.strip()
        or value.endswith(".")
        or any(character.isspace() for character in value)
    ):
        return False
    labels = value.split(".")
    return all(
        re.fullmatch(r"[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?", label)
        for label in labels
    )


def normalized_connection_host(value: str) -> str:
    """规范化数据库目标，同时拒绝 URI/zone-id 等非 host 输入。"""
    if value != value.strip() or not value or any(
        marker in value for marker in ("://", "/", "\\", "@", "%")
    ):
        raise ValueError("invalid database host")
    try:
        return ipaddress.ip_address(value).compressed
    except ValueError:
        if not valid_hostname(value):
            raise ValueError("invalid database host")
        return value.lower()


@dataclass(frozen=True)
class EgressRule:
    """一个可审计的 host 或 CIDR + port 出站授权。"""

    port: int
    hostname: str | None = None
    network: ipaddress.IPv4Network | ipaddress.IPv6Network | None = None


def split_allowlist_target(entry: str) -> tuple[str, int]:
    """解析 host:port 或 [IPv6/CIDR]:port。"""
    value = entry.strip()
    if value.startswith("["):
        closing_bracket = value.find("]")
        if closing_bracket <= 1 or value[closing_bracket + 1:closing_bracket + 2] != ":":
            raise RuntimeError("CONNECTOR_EGRESS_ALLOWLIST contains an invalid target")
        target, port_text = value[1:closing_bracket], value[closing_bracket + 2:]
    else:
        if value.count(":") != 1:
            raise RuntimeError(
                "CONNECTOR_EGRESS_ALLOWLIST IPv6 targets must use brackets",
            )
        target, port_text = value.rsplit(":", 1)
    try:
        port = int(port_text)
    except ValueError as exc:
        raise RuntimeError(
            "CONNECTOR_EGRESS_ALLOWLIST contains an invalid port",
        ) from exc
    if port < 1 or port > 65535:
        raise RuntimeError("CONNECTOR_EGRESS_ALLOWLIST contains an invalid port")
    return target.strip(), port


def parse_egress_allowlist(raw: str) -> tuple[EgressRule, ...]:
    """把逗号分隔的精确目标合同解析为不可变规则集。"""
    rules: list[EgressRule] = []
    for entry in raw.split(","):
        if not entry.strip():
            continue
        target, port = split_allowlist_target(entry)
        try:
            network = ipaddress.ip_network(target, strict=False)
        except ValueError:
            try:
                hostname = normalized_connection_host(target)
            except ValueError as exc:
                raise RuntimeError(
                    "CONNECTOR_EGRESS_ALLOWLIST contains an invalid host",
                ) from exc
            if not valid_hostname(hostname):
                raise RuntimeError(
                    "CONNECTOR_EGRESS_ALLOWLIST host entries must be DNS names",
                )
            rules.append(EgressRule(port=port, hostname=hostname))
        else:
            rules.append(EgressRule(port=port, network=network))
    return tuple(rules)


EGRESS_ALLOWLIST_RAW = os.getenv("CONNECTOR_EGRESS_ALLOWLIST", "")
if ENVIRONMENT == "production" and not EGRESS_ALLOWLIST_RAW.strip():
    raise RuntimeError("CONNECTOR_EGRESS_ALLOWLIST is required in production")
EGRESS_ALLOWLIST = parse_egress_allowlist(EGRESS_ALLOWLIST_RAW)
if ENVIRONMENT == "production" and not EGRESS_ALLOWLIST:
    raise RuntimeError(
        "CONNECTOR_EGRESS_ALLOWLIST must contain at least one IP/CIDR target",
    )
if ENVIRONMENT == "production" and any(
    rule.hostname is not None for rule in EGRESS_ALLOWLIST
):
    raise RuntimeError(
        "production CONNECTOR_EGRESS_ALLOWLIST accepts only IP/CIDR + port entries",
    )


def parse_egress_denylist(raw: str) -> tuple[
    ipaddress.IPv4Network | ipaddress.IPv6Network, ...
]:
    """解析无端口的平台控制面 CIDR；deny 永远优先于 allow。"""
    networks: list[ipaddress.IPv4Network | ipaddress.IPv6Network] = []
    for entry in raw.split(","):
        if not entry.strip():
            continue
        try:
            networks.append(ipaddress.ip_network(entry.strip(), strict=False))
        except ValueError as exc:
            raise RuntimeError(
                "CONNECTOR_EGRESS_DENYLIST must contain only IP/CIDR entries",
            ) from exc
    return tuple(networks)


EGRESS_DENYLIST_RAW = os.getenv("CONNECTOR_EGRESS_DENYLIST", "")
if ENVIRONMENT == "production" and not EGRESS_DENYLIST_RAW.strip():
    raise RuntimeError("CONNECTOR_EGRESS_DENYLIST is required in production")
EGRESS_DENYLIST = parse_egress_denylist(EGRESS_DENYLIST_RAW)
if ENVIRONMENT == "production" and not EGRESS_DENYLIST:
    raise RuntimeError(
        "CONNECTOR_EGRESS_DENYLIST must contain at least one control-plane CIDR",
    )


class EgressDeniedError(RuntimeError):
    """不携带目标值的稳定出站拒绝。"""


def forbidden_special_address(address: ipaddress.IPv4Address | ipaddress.IPv6Address) -> bool:
    """特殊地址永不允许，即使运维误把它写入白名单。"""
    metadata_addresses = {
        ipaddress.ip_address("169.254.169.254"),
        ipaddress.ip_address("fd00:ec2::254"),
    }
    return (
        (
            isinstance(address, ipaddress.IPv6Address)
            and address.ipv4_mapped is not None
        )
        or
        address in metadata_addresses
        or address.is_unspecified
        or address.is_loopback
        or address.is_link_local
        or address.is_multicast
        or address.is_reserved
        or any(address in network for network in EGRESS_DENYLIST)
    )


def address_allowed(host: str, port: int, address: ipaddress.IPv4Address | ipaddress.IPv6Address) -> bool:
    """host 规则授权其全部解析地址；CIDR 规则按地址逐个授权。"""
    for rule in EGRESS_ALLOWLIST:
        if rule.port != port:
            continue
        if (
            ENVIRONMENT != "production"
            and rule.hostname is not None
            and rule.hostname == host
        ):
            return True
        if rule.network is not None and address in rule.network:
            return True
    return False


def resolve_egress_target(host: str, port: int) -> str:
    """解析并校验全部地址，再返回一个确定 IP 供驱动直连以阻断 DNS rebinding。"""
    normalized_host = normalized_connection_host(host)
    if not EGRESS_ALLOWLIST and ENVIRONMENT != "production":
        # 本地单元测试和直接运行保留 host 语义；compose 显式配置白名单后会走
        # 与生产相同的解析与 IP pinning 路径。
        return normalized_host
    try:
        infos = socket.getaddrinfo(
            normalized_host, port, type=socket.SOCK_STREAM,
        )
    except OSError as exc:
        raise EgressDeniedError("EGRESS_TARGET_RESOLUTION_FAILED") from exc
    addresses: set[ipaddress.IPv4Address | ipaddress.IPv6Address] = set()
    for info in infos:
        try:
            addresses.add(ipaddress.ip_address(info[4][0]))
        except ValueError as exc:
            raise EgressDeniedError("EGRESS_TARGET_RESOLUTION_FAILED") from exc
    if not addresses:
        raise EgressDeniedError("EGRESS_TARGET_RESOLUTION_FAILED")
    for address in addresses:
        if forbidden_special_address(address) or not address_allowed(
            normalized_host, port, address,
        ):
            raise EgressDeniedError("EGRESS_TARGET_DENIED")
    # 固定排序使审计和连接行为可复现；地址族并不影响“所有地址均已授权”的条件。
    return sorted(
        addresses, key=lambda item: (item.version, int(item)),
    )[0].compressed


class RequestBodyLimitMiddleware:
    """在 FastAPI/Pydantic 解析凭据前限制完整 HTTP 请求体。"""

    def __init__(self, application, max_bytes: int):
        self.application = application
        self.max_bytes = max_bytes

    async def __call__(self, scope, receive, send):
        if scope["type"] != "http":
            await self.application(scope, receive, send)
            return
        content_length = next(
            (
                value
                for key, value in scope.get("headers", [])
                if key.lower() == b"content-length"
            ),
            b"",
        )
        if content_length:
            try:
                declared = int(content_length)
            except ValueError:
                declared = self.max_bytes + 1
            if declared > self.max_bytes:
                await JSONResponse(
                    status_code=413,
                    content={"detail": "CONNECTOR_REQUEST_BODY_LIMIT_EXCEEDED"},
                )(scope, receive, send)
                return
        received = 0

        async def limited_receive():
            nonlocal received
            message = await receive()
            if message.get("type") == "http.request":
                received += len(message.get("body", b""))
                if received > self.max_bytes:
                    raise HTTPException(
                        status_code=413,
                        detail="CONNECTOR_REQUEST_BODY_LIMIT_EXCEEDED",
                    )
            return message

        await self.application(scope, limited_receive, send)


app = FastAPI(title="Intelligent Report Connector Service", version="0.1.0")
app.add_middleware(RequestBodyLimitMiddleware, max_bytes=HTTP_MAX_REQUEST_BYTES)
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

    @field_validator("host")
    @classmethod
    def validate_host(cls, value: str) -> str:
        """只接受 host；出站授权与 DNS 校验在每次新建物理连接时执行。"""
        return normalized_connection_host(value)

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
        self.registry_references = 0

    def add_registry_reference(self) -> None:
        """保护 get_pool 到 acquire 之间的池引用，避免 LRU 误关。"""
        with self.condition:
            if self.closed:
                raise RuntimeError("connection pool is closed")
            self.registry_references += 1

    def release_registry_reference(self) -> None:
        """释放尚未转化为 in_use 连接的短期注册表引用。"""
        with self.condition:
            self.registry_references -= 1
            self.condition.notify_all()

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
        if not reserve_global_connection(self.config.connect_timeout_seconds):
            with self.condition:
                self.created -= 1
                self.in_use -= 1
                self.condition.notify()
            raise TimeoutError("connector global connection limit exceeded")
        try:
            return open_connection(self.config)
        except Exception:
            release_global_connections(1)
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
                finally:
                    self.created -= 1
                    release_global_connections(1)
            else: self.idle.append(connection)
            self.condition.notify()

    def close(self) -> None:
        """关闭全部空闲连接并唤醒正在等待的线程。"""
        released = 0
        with self.condition:
            self.closed = True
            for connection in self.idle:
                try: connection.close()
                except Exception: pass
            released = len(self.idle)
            self.created -= released
            self.idle.clear()
            self.condition.notify_all()
        release_global_connections(released)

    def close_if_evictable(self) -> bool:
        """只关闭无借用者、无活跃连接的池，供注册表 LRU 淘汰。"""
        with self.condition:
            if self.closed or self.in_use != 0 or self.registry_references != 0:
                return False
        self.close()
        return True

    def evict_one_idle_connection(self) -> bool:
        """关闭一条空闲连接；活跃连接永远不会成为全局配额淘汰对象。"""
        with self.condition:
            if self.closed or not self.idle:
                return False
            connection = self.idle.pop()
            self.created -= 1
        try:
            connection.close()
        except Exception:
            pass
        release_global_connections(1)
        return True


POOLS: dict[str, ConnectionPool] = {}
POOLS_LOCK = threading.Lock()
GLOBAL_CONNECTIONS = 0
GLOBAL_CONNECTIONS_CONDITION = threading.Condition()
ACTIVE_QUERIES: dict[str, Any] = {}
ACTIVE_QUERIES_LOCK = threading.Lock()


def pool_key(config: ConnectionConfig) -> str:
    """连接池键包含认证和容量参数；只保存摘要，避免明文密码进入全局字典。"""
    raw = "\x1f".join((config.source_key, config.source_type, config.host, str(config.port), config.database,
        config.username, config.password.get_secret_value(), config.oracle_connect_mode,
        str(config.max_connections_per_source),
        str(config.connect_timeout_seconds), str(config.query_timeout_seconds)))
    return hashlib.sha256(raw.encode()).hexdigest()


def get_pool(config: ConnectionConfig) -> ConnectionPool:
    """获取数据源专属连接池，并顺便清理过期空闲池。"""
    key = pool_key(config)
    with POOLS_LOCK:
        evict_idle_pools_locked()
        pool = POOLS.get(key)
        if pool is None:
            while len(POOLS) >= MAX_POOLS:
                if not evict_lru_pool_locked():
                    raise TimeoutError("connector pool limit exceeded")
            pool = ConnectionPool(config)
            POOLS[key] = pool
        pool.add_registry_reference()
        return pool


def evict_idle_pools_locked() -> None:
    """调用方必须持有 POOLS_LOCK，空闲超时的连接池会被完整关闭。"""
    now = time.monotonic()
    expired = sorted(
        (
            (pool.last_used, key)
            for key, pool in POOLS.items()
            if now - pool.last_used >= POOL_IDLE_TTL_SECONDS
        ),
    )
    for _, key in expired:
        pool = POOLS.get(key)
        if pool is not None and pool.close_if_evictable():
            POOLS.pop(key, None)


def evict_lru_pool_locked() -> bool:
    """调用方持有 POOLS_LOCK；仅淘汰完全空闲且没有待 acquire 引用的池。"""
    for _, key in sorted(
        (pool.last_used, key) for key, pool in POOLS.items()
    ):
        pool = POOLS.get(key)
        if pool is not None and pool.close_if_evictable():
            POOLS.pop(key, None)
            return True
    return False


def evict_lru_idle_connection() -> bool:
    """按池最近使用时间回收一条空闲连接，为全局硬配额让路。"""
    with POOLS_LOCK:
        for pool in sorted(
            POOLS.values(),
            key=lambda candidate: candidate.last_used,
        ):
            if pool.evict_one_idle_connection():
                return True
    return False


def reserve_global_connection(timeout_seconds: float) -> bool:
    """在创建 socket 前预留全局物理连接名额，必要时先回收 LRU 空闲连接。"""
    global GLOBAL_CONNECTIONS
    deadline = time.monotonic() + timeout_seconds
    while True:
        with GLOBAL_CONNECTIONS_CONDITION:
            if GLOBAL_CONNECTIONS < MAX_TOTAL_CONNECTIONS:
                GLOBAL_CONNECTIONS += 1
                return True
        if evict_lru_idle_connection():
            continue
        with GLOBAL_CONNECTIONS_CONDITION:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                return False
            GLOBAL_CONNECTIONS_CONDITION.wait(remaining)


def release_global_connections(count: int) -> None:
    """归还物理连接名额；零值用于简化无空闲连接的关闭路径。"""
    global GLOBAL_CONNECTIONS
    if count <= 0:
        return
    with GLOBAL_CONNECTIONS_CONDITION:
        GLOBAL_CONNECTIONS -= count
        if GLOBAL_CONNECTIONS < 0:
            raise RuntimeError("connector global connection count is inconsistent")
        GLOBAL_CONNECTIONS_CONDITION.notify_all()


@contextmanager
def one_shot_connection(config: ConnectionConfig):
    """创建受全局硬配额保护且绝不返回连接池的短连接。"""
    if not reserve_global_connection(config.connect_timeout_seconds):
        raise TimeoutError("connector global connection limit exceeded")
    connection = None
    try:
        connection = open_connection(config)
        yield connection
    finally:
        try:
            if connection is not None:
                connection.close()
        finally:
            release_global_connections(1)


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
    pool, connection, broken = None, None, False
    try:
        pool = get_pool(config)
        try:
            connection = pool.acquire()
        finally:
            pool.release_registry_reference()
        yield connection
    except BaseException:
        # StreamingResponse cancellation raises GeneratorExit/CancelledError.
        # An unbuffered cursor may then still have unread rows, so the physical
        # connection must never return to the pool.
        broken = True
        raise
    finally:
        if connection is not None and pool is not None:
            pool.release(connection, broken)
        tenant_slots.release()
        QUERY_SLOTS.release()


@contextmanager
def streaming_connection_cursor(config: ConnectionConfig):
    """为流式查询建立专用服务端游标，并保证异常时先断连接再关游标。"""
    with pooled_connection(config) as connection:
        cursor = (
            connection.cursor(pymysql.cursors.SSCursor)
            if config.source_type == "MYSQL"
            else connection.cursor()
        )
        try:
            yield connection, cursor
        except BaseException:
            # PyMySQL SSCursor.close() 会尝试排空未读结果。预算终止、取消或
            # 客户端断流时必须先关闭 socket，避免在清理路径继续无界读取。
            try:
                connection.close()
            finally:
                try:
                    cursor.close()
                except Exception:
                    pass
            raise
        else:
            cursor.close()


class QueryRequest(BaseModel):
    """定义受控只读查询及其行数、参数和取消标识。"""

    connection: ConnectionConfig
    sql: str = Field(min_length=1, max_length=100_000)
    parameters: list[Any] = Field(default_factory=list, max_length=1000)
    max_rows: int = Field(default=10_000, gt=0, le=100_000)
    query_id: str = Field(default_factory=lambda: str(uuid.uuid4()), min_length=1, max_length=100)


class StreamQueryRequest(BaseModel):
    """定义面向 PostgreSQL staging 的有界批流查询。"""

    connection: ConnectionConfig
    sql: str = Field(min_length=1, max_length=100_000)
    parameters: list[Any] = Field(default_factory=list, max_length=1000)
    batch_size: int = Field(default=1000, ge=1, le=5000)
    max_rows: int = Field(default=1_000_000, gt=0, le=5_000_000)
    query_id: str = Field(default_factory=lambda: str(uuid.uuid4()), min_length=1, max_length=100)


class CancelRequest(BaseModel):
    """标识需要中断的在途查询。"""

    query_id: str = Field(min_length=1, max_length=100)


class MetadataSampleRequest(BaseModel):
    """采样单张已发现表的少量数据，供元数据完善使用。"""

    connection: ConnectionConfig
    catalog_name: str = Field(default="", max_length=128)
    schema_name: str = Field(min_length=1, max_length=128)
    table_name: str = Field(min_length=1, max_length=128)
    columns: list[str] = Field(
        default_factory=list,
        max_length=MAX_METADATA_SAMPLE_COLUMNS,
    )
    max_rows: int = Field(default=10, ge=1, le=10)

    @field_validator("columns")
    @classmethod
    def validate_columns(cls, values: list[str]) -> list[str]:
        """显式投影必须唯一且只能包含普通数据库标识符。"""
        normalized: list[str] = []
        seen: set[str] = set()
        for value in values:
            if not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_$#]{0,127}", value):
                raise ValueError("invalid metadata sample column")
            key = value.casefold()
            if key in seen:
                raise ValueError("duplicate metadata sample column")
            seen.add(key)
            normalized.append(value)
        return normalized


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


class MetadataCollectionBudget:
    """在技术元数据 fetch 阶段共享行数和逻辑 JSON 字节预算。"""

    def __init__(self):
        self.rows = 0
        self.bytes = 0

    def consume(self, row: tuple[Any, ...]) -> None:
        self.rows += 1
        if self.rows > METADATA_SYNC_MAX_ROWS:
            raise ResourceBudgetExceeded(
                "METADATA_SYNC_ROW_LIMIT_EXCEEDED",
            )
        _, encoded = bounded_rows(
            [row],
            STREAM_MAX_CELL_BYTES,
            STREAM_MAX_ROW_BYTES,
            "METADATA_SYNC",
        )
        self.bytes += len(encoded[0]) + 1
        if self.bytes > JSON_MAX_RESPONSE_BYTES:
            raise ResourceBudgetExceeded(
                "METADATA_SYNC_RESPONSE_BYTES_EXCEEDED",
            )


def fetch(
    cursor,
    sql: str,
    parameters=None,
    budget: MetadataCollectionBudget | None = None,
) -> list[dict[str, Any]]:
    """执行内部元数据查询，并在累计结果进入内存前分批计量。"""
    cursor.execute(sql, parameters or [])
    names = [item[0].lower() for item in cursor.description or []]
    result: list[dict[str, Any]] = []
    while True:
        rows = cursor.fetchmany(1000)
        if not rows:
            break
        for row in rows:
            if budget is not None:
                budget.consume(row)
            result.append(dict(zip(names, row)))
    return result


def collect_mysql(cursor) -> list[dict[str, Any]]:
    """从 information_schema 采集当前 MySQL 数据库的技术元数据。"""
    budget = MetadataCollectionBudget()
    tables = fetch(cursor, """SELECT table_schema catalog_name, table_schema schema_name, table_name,
        table_type, COALESCE(table_comment,'') source_comment, table_rows estimated_row_count
        FROM information_schema.tables WHERE table_schema=DATABASE() ORDER BY table_name""", budget=budget)
    columns = fetch(cursor, """SELECT table_schema schema_name, table_name, column_name, ordinal_position,
        COALESCE(column_comment,'') source_comment, column_type native_type, data_type,
        character_maximum_length length, numeric_precision, numeric_scale,
        is_nullable, column_default default_value, column_key
        FROM information_schema.columns WHERE table_schema=DATABASE() ORDER BY table_name,ordinal_position""", budget=budget)
    constraints = fetch(cursor, """SELECT tc.table_schema schema_name,tc.table_name,tc.constraint_name,tc.constraint_type,
        kcu.column_name,kcu.ordinal_position,kcu.referenced_table_name,kcu.referenced_column_name
        FROM information_schema.table_constraints tc JOIN information_schema.key_column_usage kcu
        ON tc.constraint_schema=kcu.constraint_schema AND tc.table_name=kcu.table_name AND tc.constraint_name=kcu.constraint_name
        WHERE tc.constraint_schema=DATABASE() ORDER BY tc.table_name,tc.constraint_name,kcu.ordinal_position""", budget=budget)
    indexes = fetch(cursor, """SELECT table_schema schema_name,table_name,index_name,non_unique,column_name,seq_in_index
        FROM information_schema.statistics WHERE table_schema=DATABASE()
        ORDER BY table_name,index_name,seq_in_index""", budget=budget)
    return assemble_metadata(tables, columns, constraints, indexes)


def collect_oracle(cursor, schemas: list[str]) -> list[dict[str, Any]]:
    """从 Oracle 数据字典采集白名单模式下的技术元数据。"""
    budget = MetadataCollectionBudget()
    placeholders = ",".join(f":{index + 1}" for index in range(len(schemas)))
    tables = fetch(cursor, f"""SELECT o.owner catalog_name, o.owner schema_name, o.object_name table_name,
        o.object_type table_type, NVL(c.comments,'') source_comment, t.num_rows estimated_row_count
        FROM all_objects o LEFT JOIN all_tab_comments c ON c.owner=o.owner AND c.table_name=o.object_name
        LEFT JOIN all_tables t ON t.owner=o.owner AND t.table_name=o.object_name
        WHERE o.object_type IN ('TABLE','VIEW') AND o.owner IN ({placeholders}) ORDER BY o.owner,o.object_name""", schemas, budget)
    columns = fetch(cursor, f"""SELECT c.owner schema_name,c.table_name,c.column_name,c.column_id ordinal_position,
        NVL(cc.comments,'') source_comment,c.data_type native_type,c.data_type,
        c.char_length length,c.data_precision numeric_precision,c.data_scale numeric_scale,
        c.nullable is_nullable,c.data_default default_value,CAST(NULL AS VARCHAR2(3)) column_key
        FROM all_tab_columns c LEFT JOIN all_col_comments cc
        ON cc.owner=c.owner AND cc.table_name=c.table_name AND cc.column_name=c.column_name
        WHERE c.owner IN ({placeholders}) ORDER BY c.owner,c.table_name,c.column_id""", schemas, budget)
    constraints = fetch(cursor, f"""SELECT c.owner schema_name,c.table_name,c.constraint_name,
        DECODE(c.constraint_type,'P','PRIMARY KEY','U','UNIQUE','R','FOREIGN KEY','C','CHECK',c.constraint_type) constraint_type,
        cc.column_name,cc.position ordinal_position,rc.table_name referenced_table_name,rcc.column_name referenced_column_name
        FROM all_constraints c JOIN all_cons_columns cc ON cc.owner=c.owner AND cc.constraint_name=c.constraint_name
        LEFT JOIN all_constraints rc ON rc.owner=c.r_owner AND rc.constraint_name=c.r_constraint_name
        LEFT JOIN all_cons_columns rcc ON rcc.owner=rc.owner AND rcc.constraint_name=rc.constraint_name AND rcc.position=cc.position
        WHERE c.constraint_type IN ('P','U','R','C') AND c.owner IN ({placeholders})
        ORDER BY c.owner,c.table_name,c.constraint_name,cc.position""", schemas, budget)
    indexes = fetch(cursor, f"""SELECT i.table_owner schema_name,i.table_name,i.index_name,DECODE(i.uniqueness,'UNIQUE',0,1) non_unique,
        c.column_name,c.column_position seq_in_index FROM all_indexes i JOIN all_ind_columns c
        ON c.index_owner=i.owner AND c.index_name=i.index_name
        WHERE i.table_owner IN ({placeholders}) ORDER BY i.table_owner,i.table_name,i.index_name,c.column_position""", schemas, budget)
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
    constraints_by_column: dict[
        tuple[str, str, str], list[dict[str, Any]]
    ] = {}
    for (schema, table_name, _), constraint in grouped_constraints.items():
        for column_name in constraint["columns"]:
            constraints_by_column.setdefault(
                (schema, table_name, str(column_name)), [],
            ).append(constraint)
    grouped_indexes: dict[tuple[str, str, str], dict[str, Any]] = {}
    for item in indexes:
        key = (str(item.get("schema_name", "")), str(item["table_name"]), str(item["index_name"]))
        value = grouped_indexes.setdefault(key, {"name": item["index_name"], "unique": int(item["non_unique"]) == 0, "columns": []})
        value["columns"].append(item["column_name"])
    for item in columns:
        table_key = (str(item.get("schema_name", "")), str(item["table_name"]))
        table = by_table.get(table_key)
        if table is None: continue
        keys = constraints_by_column.get(
            (
                table_key[0],
                table_key[1],
                str(item["column_name"]),
            ),
            [],
        )
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


def authorize_connection_test(x_connector_token: str = Header(default="")) -> None:
    """连接测试令牌只能挂载到轻量测试端点；通用令牌保留兼容能力。"""
    if not CONNECTION_TEST_TOKEN or x_connector_token not in (
        CONNECTION_TEST_TOKEN,
        INTERNAL_TOKEN,
    ):
        raise HTTPException(status_code=401, detail="invalid connector token")


def oracle_dsn(config: ConnectionConfig, pinned_host: str | None = None) -> str:
    """按 SID 或服务名模式构造 Oracle DSN。"""
    host = pinned_host or config.host
    if config.oracle_connect_mode == "SID":
        return oracledb.makedsn(host, config.port, sid=config.database)
    return oracledb.makedsn(host, config.port, service_name=config.database)


def open_connection(config: ConnectionConfig):
    """创建配置了连接和查询超时的 MySQL 或 Oracle 物理连接。"""
    pinned_host = resolve_egress_target(config.host, config.port)
    password = config.password.get_secret_value()
    if config.source_type == "MYSQL":
        return pymysql.connect(
            host=pinned_host, port=config.port, user=config.username, password=password,
            database=config.database, connect_timeout=int(config.connect_timeout_seconds),
            read_timeout=int(config.query_timeout_seconds), write_timeout=int(config.query_timeout_seconds),
            charset="utf8mb4", cursorclass=pymysql.cursors.Cursor, autocommit=True,
        )
    connection = oracledb.connect(
        user=config.username, password=password,
        dsn=oracle_dsn(config, pinned_host),
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
        "INSERT", "UPDATE", "DELETE", "MERGE", "UPSERT", "CREATE", "ALTER", "DROP",
        "TRUNCATE", "RENAME", "GRANT", "REVOKE", "CALL", "EXEC", "EXECUTE", "BEGIN", "COMMIT",
        "ROLLBACK", "SAVEPOINT", "LOCK", "UNLOCK", "OUTFILE", "DUMPFILE", "LOAD_FILE",
        "GET_LOCK", "RELEASE_LOCK", "SLEEP", "BENCHMARK",
    }
    rejected = forbidden.intersection(tokens)
    if "REPLACE" in tokens and not sql_keyword_occurrences_are_function_calls(normalized, "REPLACE"):
        rejected.add("REPLACE")
    if rejected:
        raise HTTPException(status_code=400, detail="query contains a forbidden operation")


def quoted_identifier(value: str, source_type: str) -> str:
    """只接受数据库元数据可返回的普通标识符，再按方言安全引用。"""
    if not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_$#]{0,127}", value):
        raise HTTPException(status_code=400, detail="invalid table identifier")
    return f"`{value}`" if source_type == "MYSQL" else f'"{value}"'


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


def sql_keyword_occurrences_are_function_calls(sql: str, keyword: str) -> bool:
    """确认引号外的同名词元都紧跟左括号，避免把 SQL 函数误判为写操作。"""
    current, quote, index = [], None, 0
    normalized_keyword = keyword.upper()
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
            if current and "".join(current).upper() == normalized_keyword:
                return False
            current = []
            quote = char
        elif char.isalnum() or char in ("_", "$"):
            current.append(char)
        elif current:
            if "".join(current).upper() == normalized_keyword:
                lookahead = index
                while lookahead < len(sql) and sql[lookahead].isspace():
                    lookahead += 1
                if lookahead >= len(sql) or sql[lookahead] != "(":
                    return False
            current = []
        index += 1
    return not current or "".join(current).upper() != normalized_keyword


class ResourceBudgetExceeded(RuntimeError):
    """携带稳定代码但绝不携带业务值的资源预算异常。"""

    def __init__(self, code: str):
        super().__init__(code)
        self.code = code


class StreamTerminated(RuntimeError):
    """触发连接池销毁未排空的物理连接，并在外层转换为稳定流错误。"""

    def __init__(self, code: str):
        super().__init__(code)
        self.code = code


def json_scalar_bytes(value: Any, unsupported_code: str) -> bytes:
    """仅序列化数据库可预期的标量，未知驱动对象不会隐式调用 str。"""
    if isinstance(value, float) and not math.isfinite(value):
        raise ResourceBudgetExceeded(unsupported_code)
    if isinstance(value, Decimal) and not value.is_finite():
        raise ResourceBudgetExceeded(unsupported_code)
    if value is None or isinstance(value, (str, bool, int, float, Decimal)):
        return json.dumps(
            value, ensure_ascii=False, separators=(",", ":"), default=str,
        ).encode("utf-8")
    if isinstance(value, (date, datetime, datetime_time)):
        return json.dumps(
            value.isoformat(), ensure_ascii=False, separators=(",", ":"),
        ).encode("utf-8")
    if isinstance(value, (bytes, bytearray, memoryview)):
        raise ResourceBudgetExceeded(unsupported_code)
    # Oracle LOB、游标和其他未知驱动对象均拒绝；不调用其 __str__，避免读取或回显。
    raise ResourceBudgetExceeded(unsupported_code)


def bounded_rows(
    rows: list[tuple[Any, ...]] | list[list[Any]],
    max_cell_bytes: int,
    max_row_bytes: int,
    code_prefix: str,
) -> tuple[list[list[Any]], list[bytes]]:
    """验证并一次编码每个单元格/行，供响应和 NDJSON 复用。"""
    normalized_rows: list[list[Any]] = []
    encoded_rows: list[bytes] = []
    for row in rows:
        values: list[Any] = []
        encoded_cells: list[bytes] = []
        for value in row:
            encoded = json_scalar_bytes(
                value, f"{code_prefix}_VALUE_UNSUPPORTED",
            )
            if len(encoded) > max_cell_bytes:
                raise ResourceBudgetExceeded(
                    f"{code_prefix}_CELL_BYTES_EXCEEDED",
                )
            encoded_cells.append(encoded)
            # JSON-safe representation is derived from the encoded scalar so no
            # second implicit driver conversion can occur in FastAPI.
            values.append(json.loads(encoded))
        encoded_row = b"[" + b",".join(encoded_cells) + b"]"
        if len(encoded_row) > max_row_bytes:
            raise ResourceBudgetExceeded(
                f"{code_prefix}_ROW_BYTES_EXCEEDED",
            )
        normalized_rows.append(values)
        encoded_rows.append(encoded_row)
    return normalized_rows, encoded_rows


def metadata_sample_projection(
    cursor,
    request: MetadataSampleRequest,
) -> list[str]:
    """从源端类型元数据构建非 LOB/非二进制显式投影。"""
    source_type = request.connection.source_type
    if source_type == "MYSQL":
        cursor.execute(
            """SELECT column_name, data_type, column_type
               FROM information_schema.columns
               WHERE table_schema=%s AND table_name=%s
               ORDER BY ordinal_position""",
            (request.schema_name, request.table_name),
        )
    else:
        cursor.execute(
            """SELECT column_name, data_type, data_type
               FROM all_tab_columns
               WHERE owner=:1 AND table_name=:2
               ORDER BY column_id""",
            (request.schema_name.upper(), request.table_name.upper()),
        )
    discovered = cursor.fetchall()
    safe: dict[str, str] = {}
    for name, data_type, column_type in discovered:
        data_kind = " ".join(str(data_type or "").upper().split())
        column_kind = " ".join(str(column_type or "").upper().split())
        if (
            data_kind.endswith("BLOB")
            or data_kind in {
                "BFILE", "BINARY", "VARBINARY", "RAW", "LONG RAW",
                "CLOB", "NCLOB", "LONG", "XMLTYPE", "JSON",
                "LONGTEXT", "MEDIUMTEXT",
            }
            or column_kind.startswith(("BINARY(", "VARBINARY("))
        ):
            continue
        text_name = str(name)
        safe[text_name.casefold()] = text_name
    requested = request.columns or list(safe.values())
    if len(requested) > MAX_METADATA_SAMPLE_COLUMNS:
        raise ResourceBudgetExceeded(
            "METADATA_SAMPLE_COLUMN_LIMIT_EXCEEDED",
        )
    projection: list[str] = []
    for name in requested:
        actual = safe.get(name.casefold())
        if actual is None:
            raise ResourceBudgetExceeded(
                "METADATA_SAMPLE_COLUMN_UNSAFE",
            )
        projection.append(actual)
    return projection


def metadata_sample_response(columns: list[str], rows: list[tuple[Any, ...]]) -> Response:
    """构造精确计量的样本响应，不经 FastAPI 二次隐式序列化。"""
    _, encoded_rows = bounded_rows(
        rows,
        METADATA_SAMPLE_MAX_CELL_BYTES,
        METADATA_SAMPLE_MAX_ROW_BYTES,
        "METADATA_SAMPLE",
    )
    encoded_columns = json.dumps(
        columns, ensure_ascii=False, separators=(",", ":"),
    ).encode("utf-8")
    content = (
        b'{"columns":' + encoded_columns
        + b',"rows":[' + b",".join(encoded_rows)
        + b'],"rowCount":' + str(len(rows)).encode("ascii") + b"}"
    )
    if len(content) > METADATA_SAMPLE_MAX_RESPONSE_BYTES:
        raise ResourceBudgetExceeded(
            "METADATA_SAMPLE_RESPONSE_BYTES_EXCEEDED",
        )
    return Response(
        content=content,
        status_code=200,
        media_type="application/json",
        headers={"Cache-Control": "no-store"},
    )


def bounded_json_document_response(
    payload: dict[str, Any],
    max_bytes: int,
    limit_code: str,
) -> Response:
    """对非流式 Connector 响应使用精确的整文档字节上限。"""
    content = json.dumps(
        payload,
        ensure_ascii=False,
        separators=(",", ":"),
        default=str,
    ).encode("utf-8")
    if len(content) > max_bytes:
        raise ResourceBudgetExceeded(limit_code)
    return Response(
        content=content,
        status_code=200,
        media_type="application/json",
        headers={"Cache-Control": "no-store"},
    )


def encode_stream_event(event: dict[str, Any]) -> bytes:
    """编码不含未知驱动对象的 NDJSON 控制事件。"""
    return (
        json.dumps(
            event, ensure_ascii=False, separators=(",", ":"),
        ).encode("utf-8")
        + b"\n"
    )


def stream_error_event(code: str) -> bytes:
    """错误事件只返回枚举代码，不包含驱动文本、SQL 或数据值。"""
    return encode_stream_event({"type": "error", "code": code})


@app.get("/health/live")
def live() -> dict[str, str]:
    """返回不依赖外部数据库的进程存活状态。"""
    return {"status": "live"}


@app.post(
    "/v1/connections/test",
    dependencies=[Depends(authorize_connection_test)],
)
def test_connection(config: ConnectionConfig) -> dict[str, Any]:
    """执行轻量版本查询，验证连接并报告往返延迟。"""
    started = time.perf_counter()
    try:
        # 连接测试不进入普通查询池：草稿测试无论成功或失败都 one-shot 关闭，
        # 避免大量不同配置在 TTL 窗口内累积空闲数据库会话。
        with one_shot_connection(config) as connection, closing(connection.cursor()) as cursor:
            cursor.execute("SELECT VERSION()" if config.source_type == "MYSQL" else "SELECT BANNER FROM V$VERSION WHERE ROWNUM = 1")
            version = str(cursor.fetchone()[0])
    except Exception as exc:
        raise HTTPException(status_code=502, detail="CONNECTION_TEST_FAILED") from exc
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
def sync_metadata(config: ConnectionConfig) -> Response:
    """采集元数据并返回带时间水位和稳定哈希的完整快照。"""
    try:
        with streaming_connection_cursor(config) as (connection, cursor):
            assets = collect_mysql(cursor) if config.source_type == "MYSQL" else collect_oracle(cursor, config.schemas or [config.username.upper()])
    except ResourceBudgetExceeded as exc:
        raise HTTPException(status_code=413, detail=exc.code) from exc
    except Exception as exc:
        raise HTTPException(status_code=502, detail="METADATA_SYNC_FAILED") from exc
    snapshot_json = json.dumps(assets, sort_keys=True, separators=(",", ":"), default=str)
    try:
        return bounded_json_document_response(
            {
                "assets": len(assets),
                "watermark": datetime.now(timezone.utc).isoformat(),
                "snapshotHash": hashlib.sha256(snapshot_json.encode()).hexdigest(),
                "tables": assets,
            },
            JSON_MAX_RESPONSE_BYTES,
            "METADATA_SYNC_RESPONSE_BYTES_EXCEEDED",
        )
    except ResourceBudgetExceeded as exc:
        raise HTTPException(status_code=413, detail=exc.code) from exc


@app.post("/v1/metadata/sample", dependencies=[Depends(authorize)])
def sample_metadata(request: MetadataSampleRequest) -> Response:
    """以非 LOB 显式投影采集最多十行，并在响应序列化前执行字节预算。"""
    source_type = request.connection.source_type
    schema = quoted_identifier(request.schema_name, source_type)
    table = quoted_identifier(request.table_name, source_type)
    try:
        with pooled_connection(request.connection) as connection, closing(connection.cursor()) as cursor:
            projection = metadata_sample_projection(cursor, request)
            if not projection:
                return metadata_sample_response([], [])
            selected = ",".join(
                quoted_identifier(column, source_type)
                for column in projection
            )
            sql = (
                f"SELECT {selected} FROM {schema}.{table} LIMIT {request.max_rows}"
                if source_type == "MYSQL"
                else f"SELECT {selected} FROM {schema}.{table} "
                f"FETCH FIRST {request.max_rows} ROWS ONLY"
            )
            cursor.execute(sql)
            columns = [str(item[0]) for item in cursor.description or []]
            rows = cursor.fetchmany(request.max_rows)
            return metadata_sample_response(columns, rows)
    except ResourceBudgetExceeded as exc:
        raise HTTPException(status_code=413, detail=exc.code) from exc
    except Exception as exc:
        raise HTTPException(status_code=502, detail="METADATA_SAMPLE_FAILED") from exc


@app.post("/v1/query", dependencies=[Depends(authorize)])
def query(request: QueryRequest) -> Response:
    """用服务端游标增量构造有界 JSON，不先把 max_rows 全部装入内存。"""
    validate_read_only_sql(request.sql)
    started = time.perf_counter()
    try:
        with streaming_connection_cursor(request.connection) as (connection, cursor):
            with ACTIVE_QUERIES_LOCK:
                if request.query_id in ACTIVE_QUERIES: raise HTTPException(status_code=409, detail="query id is already active")
                ACTIVE_QUERIES[request.query_id] = (request.connection.source_type, connection)
            try:
                cursor.execute(request.sql, request.parameters)
                columns = [str(item[0]) for item in cursor.description or []]
                if len(columns) > 1600:
                    raise ResourceBudgetExceeded(
                        "QUERY_COLUMN_LIMIT_EXCEEDED",
                    )
                encoded_columns = json.dumps(
                    columns, ensure_ascii=False, separators=(",", ":"),
                ).encode("utf-8")
                prefix = b'{"columns":' + encoded_columns + b',"rows":['
                # 为 ],"rowCount":...,"durationMs":...} 预留固定空间。
                if len(prefix) + 128 > JSON_MAX_RESPONSE_BYTES:
                    raise ResourceBudgetExceeded(
                        "QUERY_RESPONSE_BYTES_EXCEEDED",
                    )
                encoded_rows: list[bytes] = []
                payload_bytes = len(prefix)
                row_count = 0
                fetch_size = max(
                    1,
                    min(
                        100,
                        request.max_rows + 1,
                        JSON_MAX_RESPONSE_BYTES // STREAM_MAX_ROW_BYTES,
                    ),
                )
                while True:
                    batch = cursor.fetchmany(fetch_size)
                    if not batch:
                        break
                    for row in batch:
                        row_count += 1
                        if row_count > request.max_rows:
                            raise StreamTerminated(
                                "QUERY_ROW_LIMIT_EXCEEDED",
                            )
                        _, encoded = bounded_rows(
                            [row],
                            STREAM_MAX_CELL_BYTES,
                            STREAM_MAX_ROW_BYTES,
                            "QUERY",
                        )
                        separator_bytes = 1 if encoded_rows else 0
                        if (
                            payload_bytes + separator_bytes + len(encoded[0]) + 128
                            > JSON_MAX_RESPONSE_BYTES
                        ):
                            raise ResourceBudgetExceeded(
                                "QUERY_RESPONSE_BYTES_EXCEEDED",
                            )
                        encoded_rows.append(encoded[0])
                        payload_bytes += separator_bytes + len(encoded[0])
                suffix = (
                    b'],"rowCount":' + str(row_count).encode("ascii")
                    + b',"durationMs":'
                    + str(
                        int((time.perf_counter() - started) * 1000),
                    ).encode("ascii")
                    + b"}"
                )
                content = prefix + b",".join(encoded_rows) + suffix
                if len(content) > JSON_MAX_RESPONSE_BYTES:
                    raise ResourceBudgetExceeded(
                        "QUERY_RESPONSE_BYTES_EXCEEDED",
                    )
            finally:
                with ACTIVE_QUERIES_LOCK: ACTIVE_QUERIES.pop(request.query_id, None)
    except HTTPException:
        raise
    except StreamTerminated as exc:
        raise HTTPException(status_code=413, detail=exc.code) from exc
    except ResourceBudgetExceeded as exc:
        raise HTTPException(status_code=413, detail=exc.code) from exc
    except Exception as exc:
        raise HTTPException(status_code=502, detail="QUERY_FAILED") from exc
    return Response(
        content=content,
        status_code=200,
        media_type="application/json",
        headers={"Cache-Control": "no-store"},
    )


def stream_query_events(request: StreamQueryRequest) -> Iterator[bytes]:
    """以服务端游标逐批输出 NDJSON，并对单元格、行和整流字节双重限流。"""
    started, row_count, emitted_bytes = time.perf_counter(), 0, 0
    try:
        with streaming_connection_cursor(request.connection) as (connection, cursor):
            with ACTIVE_QUERIES_LOCK:
                if request.query_id in ACTIVE_QUERIES:
                    yield stream_error_event("QUERY_ID_CONFLICT")
                    return
                ACTIVE_QUERIES[request.query_id] = (request.connection.source_type, connection)
            try:
                cursor.execute(request.sql, request.parameters)
                columns = [str(item[0]) for item in cursor.description or []]
                schema_event = encode_stream_event(
                    {"type": "schema", "columns": columns},
                )
                if len(schema_event) > STREAM_MAX_BYTES - STREAM_TERMINAL_RESERVE_BYTES:
                    raise StreamTerminated("QUERY_SOURCE_BYTES_EXCEEDED")
                emitted_bytes += len(schema_event)
                yield schema_event
                # fetchmany 在 PyMySQL SSCursor 上是真正的服务端增量读取。用最坏
                # 单行预算限制每次驱动分配，事件也始终小于 Scanner 的硬上限。
                fetch_size = max(
                    1,
                    min(
                        request.batch_size,
                        STREAM_EVENT_MAX_BYTES // STREAM_MAX_ROW_BYTES,
                    ),
                )
                while True:
                    rows = cursor.fetchmany(fetch_size)
                    if not rows:
                        break
                    row_count += len(rows)
                    if row_count > request.max_rows:
                        raise StreamTerminated("QUERY_ROW_LIMIT_EXCEEDED")
                    _, encoded_rows = bounded_rows(
                        rows,
                        STREAM_MAX_CELL_BYTES,
                        STREAM_MAX_ROW_BYTES,
                        "QUERY",
                    )
                    batch_event = (
                        b'{"type":"batch","rows":['
                        + b",".join(encoded_rows)
                        + b"]}\n"
                    )
                    if len(batch_event) > STREAM_EVENT_MAX_BYTES:
                        raise StreamTerminated(
                            "QUERY_STREAM_EVENT_BYTES_EXCEEDED",
                        )
                    if (
                        emitted_bytes + len(batch_event)
                        > STREAM_MAX_BYTES - STREAM_TERMINAL_RESERVE_BYTES
                    ):
                        raise StreamTerminated(
                            "QUERY_SOURCE_BYTES_EXCEEDED",
                        )
                    emitted_bytes += len(batch_event)
                    yield batch_event
            finally:
                with ACTIVE_QUERIES_LOCK:
                    ACTIVE_QUERIES.pop(request.query_id, None)
    except StreamTerminated as exc:
        yield stream_error_event(exc.code)
        return
    except ResourceBudgetExceeded as exc:
        yield stream_error_event(exc.code)
        return
    except Exception:
        yield stream_error_event("QUERY_FAILED")
        return
    complete_event = encode_stream_event({
        "type": "complete", "rowCount": row_count,
        "durationMs": int((time.perf_counter() - started) * 1000),
    })
    if emitted_bytes + len(complete_event) > STREAM_MAX_BYTES:
        yield stream_error_event("QUERY_SOURCE_BYTES_EXCEEDED")
        return
    yield complete_event


@app.post("/v1/query/stream", dependencies=[Depends(authorize)])
def stream_query(request: StreamQueryRequest) -> StreamingResponse:
    """流式返回 schema、批次和完成事件，供数据面直接 COPY 到 staging。"""
    validate_read_only_sql(request.sql)
    return StreamingResponse(
        stream_query_events(request),
        media_type="application/x-ndjson",
        headers={"Cache-Control": "no-store", "X-Accel-Buffering": "no"},
    )
