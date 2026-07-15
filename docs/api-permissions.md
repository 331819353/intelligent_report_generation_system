# 权限 API

所有权限均在服务端从访问令牌解析出的租户和用户上下文中评估，客户端不能指定或覆盖 `tenant_id`。

## 权限模型

- 功能权限：用户角色关联 `resource_type + action`。
- 对象权限：可将特定对象的操作授权给用户或角色。
- 角色：平台管理员、租户管理员、数据管理员、分析师、报告设计师、查看者。
- 判定：任一有效角色具有功能权限，或用户/有效角色具有对应对象授权，即允许访问；其余统一返回 `403 PERMISSION_DENIED`。

## 评估接口

`POST /api/v1/permissions/evaluate`

```json
{
  "resourceType": "REPORT",
  "action": "UPDATE",
  "objectId": "550e8400-e29b-41d4-a716-446655440000"
}
```

响应：`{"allowed":true}`。该接口必须携带有效 Bearer Token，租户与用户身份只取自令牌。

业务接口可使用统一权限中间件 `access.Require`。对象 ID 提取函数为空时执行功能权限检查，提供对象 ID 时同时考虑对象授权。

## 权限管理接口

以下接口必须携带有效 Bearer Token，并通过 `USER:MANAGE` 权限检查。租户和操作者只能来自服务端认证上下文。

| 方法 | 地址 | 用途 |
|---|---|---|
| `GET` | `/api/v1/roles` | 查询当前租户角色 |
| `POST` | `/api/v1/roles` | 创建自定义角色 |
| `PUT` | `/api/v1/roles/{id}/permissions` | 以权限编码集合替换角色权限 |
| `POST` | `/api/v1/users/{id}/roles` | 为当前租户用户分配角色 |
| `DELETE` | `/api/v1/users/{id}/roles/{roleId}` | 撤销用户角色 |
| `POST` | `/api/v1/object-permissions` | 向用户或角色授予对象操作权限 |
| `DELETE` | `/api/v1/object-permissions/{id}` | 撤销对象权限 |

数据资产中心新增 `DATA_ASSET:READ` 和 `DATA_ASSET:MANAGE`。默认 Seed 中数据管理员拥有读写权限，分析师与报告设计师拥有读取权限，查看者仍只拥有报告读取权限。

对象授权请求：

```json
{
  "subjectType": "USER",
  "subjectId": "用户 UUID",
  "objectType": "REPORT",
  "objectId": "报告 UUID",
  "action": "READ"
}
```

数据库会再次校验 `USER` 或 `ROLE` 主体确实属于当前租户。授权、撤销、角色创建、权限替换和角色分配均写入不可变审计日志。
