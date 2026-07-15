# 身份认证 API

基础路径：`/api/v1/auth`

## 登录

`POST /login`

```json
{
  "tenantCode": "demo",
  "email": "admin@example.com",
  "password": "Admin123!"
}
```

成功返回访问令牌、轮换刷新令牌及各自过期时间。登录失败统一返回 `INVALID_CREDENTIALS`，不暴露租户、账号或密码具体哪一项错误。

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

租户 ID 只从服务端验证通过的访问令牌写入请求上下文，不接受客户端通过查询参数或普通请求头覆盖。每次受保护请求都会校验用户状态、`token_version` 和会话撤销状态。

## 安全规则

- 密码使用 bcrypt，成本通过 `AUTH_PASSWORD_BCRYPT_COST` 配置；
- 数据库只保存刷新令牌的 SHA-256 哈希；
- 刷新令牌每次使用后轮换；
- 访问令牌使用 HS256，生产密钥必须来自密钥管理系统且不少于 32 字符；
- 禁用用户、提升 `token_version` 或撤销会话都会使访问令牌失效；
- 成功登录、失败登录和退出均写入不可变审计日志；
- 前端当前将令牌保存在 `sessionStorage`，正式公网发布前应迁移为同站 HttpOnly/Secure/SameSite Cookie 或等价的 BFF 会话方案。

## 本地开发账号

执行 `make seed-dev` 创建或刷新 `.env.example` 中的演示租户和管理员。该账号仅用于本地环境，生产环境禁止运行开发 Seed。
