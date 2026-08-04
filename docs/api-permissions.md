# 权限 API

所有权限均在服务端从访问令牌解析出的租户、用户和当前领域上下文中评估，客户端不能指定或覆盖 `tenant_id`、用户身份或领域身份。

## 固定三级权限模型

权限按“平台—领域—用户”三级固定，不提供自定义角色、权限能力勾选或对象级授权接口。

| 身份 | 固定能力 | 边界 |
|---|---|---|
| 平台管理员 | 管理平台管理员、创建/启停领域、指定领域管理员、调整用户领域归属 | 只有控制面权限，不自动获得任意领域的数据访问权 |
| 领域管理员 | 管理本领域数据源、数据资产和数据集，审批发布，审批用户加入 | 只在明确担任管理员的领域内生效 |
| 领域用户 | 配置和查看所在领域的数据源、数据资产和数据集，提交配置等待发布 | 不能发布，不能访问未加入领域的数据 |

用户与领域是多对多关系，但每条业务资源只能绑定一个 `domain_id`。数据库 RLS 强制领域隔离；历史 `PLATFORM` 资产共享会在升级时收敛为 `DOMAIN`，新请求只接受 `PRIVATE` 或 `DOMAIN`。

权限判定顺序：

1. 平台控制面操作要求固定 `platform_admin` 身份。
2. 数据面操作必须存在当前有效领域和有效成员关系。
3. `DOMAIN_ADMIN` 可以执行本领域全部数据操作；`MEMBER` 只能执行 `READ` 与 `MANAGE`，不能执行 `PUBLISH`。

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

移除成员后允许用户暂时不属于任何领域；如果目标成员是领域管理员，必须先由平台管理员完成替换。任何领域都不能被配置为零管理员。

## 平台与用户管理

以下接口只接受平台管理员调用。它们调整固定身份和领域归属，不接受权限代码。

| 方法 | 地址 | 用途 |
|---|---|---|
| `GET` | `/api/v1/users` | 查询用户的平台身份与领域归属 |
| `PUT` | `/api/v1/users/{id}/platform-administrator` | 设置或移除平台管理员身份 |
| `POST` | `/api/v1/users/{id}/domains` | 将用户加入领域 |
| `DELETE` | `/api/v1/users/{id}/domains/{domainId}` | 从领域移除普通成员 |

设置平台管理员身份：

```json
{"enabled":true}
```

系统始终至少保留一位平台管理员。平台身份变更、领域创建/启停、管理员替换和用户领域归属变更均写入不可变审计日志。

## 评估接口

`POST /api/v1/permissions/evaluate` 供前端在展示操作前评估固定权限，服务端业务接口仍会再次鉴权。

```json
{
  "resourceType": "DATASET",
  "action": "MANAGE",
  "objectId": "550e8400-e29b-41d4-a716-446655440000"
}
```

响应：`{"allowed":true}`。`objectId` 仅用于兼容调用合同，不会产生对象级例外授权。
