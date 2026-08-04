# 身份认证 API

基础路径：`/api/v1/auth`

## 登录

`POST /login`

```json
{
  "email": "admin@example.com",
  "password": "Admin123!"
}
```

成功返回访问令牌、轮换刷新令牌及各自过期时间。登录失败统一返回 `INVALID_CREDENTIALS`，不暴露账号或密码具体哪一项错误。用户没有任何领域成员关系时仍可登录，以便访问领域目录并提交入域申请；此时不能调用数据源、数据集或资产接口。平台内部工作区由服务端唯一确定，客户端不提交或感知工作区标识。

## 注册

`POST /register`

```json
{
  "displayName": "张三",
  "email": "zhangsan@example.com",
  "password": "DataSource9!"
}
```

密码必须为 10–128 位，同时包含大写字母、小写字母和数字，且不能含空白或控制字符。注册成功返回与登录相同的令牌对。账号创建、默认业务域成员关系和注册审计在同一事务中完成，任一步失败都不会留下半个账号。

平台设置 `selfRegistrationEnabled` 控制是否允许自助注册。注册用户加入领域后按固定“领域用户”身份获得数据配置能力，但不具备发布审批权限；平台管理员和领域管理员都只能从已注册用户中选择。发布由领域管理员完成。历史 `selfRegistrationRoleCode` 设置仅为升级兼容保留，不再开放角色或权限能力的自由配置。

## 刷新令牌

`POST /refresh`

```json
{"refreshToken": "<refresh-token>"}
```

刷新成功后旧刷新令牌立即失效，重复使用返回 401。

## 退出

`POST /logout`

```json
{"refreshToken": "<refresh-token>"}
```

退出会撤销服务端会话；该会话签发的访问令牌也会立即失效。

## 当前身份

`GET /me`

```http
Authorization: Bearer <access-token>
```

内部工作区标识只由服务端写入请求上下文，不通过接口返回，也不接受客户端通过查询参数或普通请求头覆盖。每次受保护请求都会校验用户状态、`token_version` 和会话撤销状态。

## 安全规则

- 密码使用 bcrypt，成本通过 `AUTH_PASSWORD_BCRYPT_COST` 配置；
- 数据库只保存刷新令牌的 SHA-256 哈希；
- 刷新令牌每次使用后轮换；
- 访问令牌使用 HS256，生产密钥必须来自密钥管理系统且不少于 32 字符；
- 禁用用户、提升 `token_version` 或撤销会话都会使访问令牌失效；
- 当前领域被停用或成员关系被撤销时，会话降级为“无领域”控制面会话，不继续携带失效领域，也不会获得其他领域数据；
- 注册、成功登录、失败登录和退出均写入不可变审计日志；
- 前端当前将令牌保存在 `sessionStorage`，正式公网发布前应迁移为同站 HttpOnly/Secure/SameSite Cookie 或等价的 BFF 会话方案。

## 本地开发账号

执行 `make seed-dev` 创建或刷新 `.env.example` 中的演示平台管理员。该账号仅用于本地环境，生产环境禁止运行开发 Seed。
