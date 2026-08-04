# 权限 API

所有权限均在服务端从访问令牌解析出的租户和用户上下文中评估，客户端不能指定或覆盖 `tenant_id`。

## 权限模型

- 领域栅栏：用户与领域是多对多关系，但每条业务资源只能绑定一个 `domain_id`；领域之间不允许共享或对象授权穿透。
- 领域角色：`MEMBER` 为普通成员，`DOMAIN_ADMIN` 为领域管理员。领域管理员必须由平台管理员显式指定。
- 功能权限：用户角色关联 `resource_type + action`。
- 对象权限：可将特定对象的操作授权给用户或角色。
- 系统角色：平台管理员、租户管理员、数据管理员、数据查看者。
- 判定：先校验当前领域成员资格，再评估功能权限或对象权限。`DOMAIN_ADMIN` 可以管理本领域资源，但不能访问其他领域。平台管理员默认只有控制面权限，没有未被指定领域的数据访问权。

数据源、数资产和数据集由数据库 RLS 强制校验当前 `domain_id`。历史 `PLATFORM` 资产共享会在升级时收敛为 `DOMAIN`，新请求只接受 `PRIVATE` 或 `DOMAIN`。

## 领域管理与入域申请

无领域用户可以正常登录，但只能访问领域目录和申请控制面；数据面接口返回 `403 BUSINESS_DOMAIN_REQUIRED`。

| 方法 | 地址 | 权限 | 用途 |
|---|---|---|---|
| `GET` | `/api/v1/domains` | 已登录 | 查询本人已加入的领域 |
| `GET` | `/api/v1/domain-catalog` | 已登录 | 查询可申请领域及本人申请状态 |
| `POST` | `/api/v1/domains/{id}/applications` | 已登录 | 申请加入领域 |
| `GET` | `/api/v1/domain-applications` | 已登录 | 查询本人的申请历史 |
| `GET` | `/api/v1/domains/{id}/applications` | 领域管理员 | 查询本领域待审批申请 |
| `POST` | `/api/v1/domain-applications/{id}/decision` | 领域管理员 | 通过或驳回申请 |
| `GET` | `/api/v1/managed-domains` | 平台管理员 | 查询完整领域目录 |
| `POST` | `/api/v1/domains` | 平台管理员 | 创建领域并指定至少一位领域管理员 |
| `PATCH` | `/api/v1/domains/{id}` | 平台管理员 | 启用或停用领域 |
| `PUT` | `/api/v1/domains/{id}/administrators` | 平台管理员 | 完整替换领域管理员集合 |

创建领域请求：

```json
{
  "code": "customer-operations",
  "name": "客户运营",
  "description": "客户增长与留存分析",
  "administratorUserIds": ["用户 UUID"]
}
```

入域申请与审批：

```json
{"reason":"负责客户留存周报，需要使用该领域资产"}
```

```json
{"decision":"APPROVED","comment":"已确认项目职责"}
```

移除成员后允许用户暂时不属于任何领域；如果目标成员是领域管理员，必须先由平台管理员完成替换或降级。任何领域都不能通过管理接口被配置为零管理员。

## 评估接口

`POST /api/v1/permissions/evaluate`

```json
{
  "resourceType": "DATASET",
  "action": "MANAGE",
  "objectId": "550e8400-e29b-41d4-a716-446655440000"
}
```

响应：`{"allowed":true}`。该接口必须携带有效 Bearer Token，租户与用户身份只取自令牌。

业务接口可使用统一权限中间件 `access.Require`。对象 ID 提取函数为空时执行功能权限检查，提供对象 ID 时同时考虑对象授权。

## 权限管理接口

以下租户级角色接口必须携带有效 Bearer Token，并通过 `USER:MANAGE` 权限检查。租户和操作者只能来自服务端认证上下文。

| 方法 | 地址 | 用途 |
|---|---|---|
| `GET` | `/api/v1/roles` | 查询当前租户角色 |
| `POST` | `/api/v1/roles` | 创建自定义角色 |
| `PUT` | `/api/v1/roles/{id}/permissions` | 以权限编码集合替换角色权限 |
| `POST` | `/api/v1/users/{id}/roles` | 为当前租户用户分配角色 |
| `DELETE` | `/api/v1/users/{id}/roles/{roleId}` | 撤销用户角色 |
| `POST` | `/api/v1/object-permissions` | 向用户或角色授予对象操作权限 |
| `DELETE` | `/api/v1/object-permissions/{id}` | 撤销对象权限 |

数据资产使用 `DATA_ASSET:READ` 和 `DATA_ASSET:MANAGE`。默认 Seed 中数据管理员拥有读写权限，数据查看者拥有读取权限。

对象授权请求：

```json
{
  "subjectType": "USER",
  "subjectId": "用户 UUID",
  "objectType": "DATASET",
  "objectId": "数据集 UUID",
  "action": "READ"
}
```

数据库会再次校验 `USER` 或 `ROLE` 主体确实属于当前租户。授权、撤销、角色创建、权限替换和角色分配均写入不可变审计日志。
