# Pigsty Console API 契约（M0–M1）

前后端共同遵守。后端用 huma v2 实现，并在 `GET /api/v1/openapi.json` 暴露 OpenAPI 3.1；前端以该文件生成类型。字段名一律 camelCase。

- 基础路径：`/api/v1`
- 默认监听：`127.0.0.1:8860`（前端开发服务器把 `/api` 代理到这里）
- 认证：登录后服务端设置 HttpOnly cookie `pc_session`；未登录访问受保护接口返回 401
- 写请求（POST/PUT/DELETE）校验 `Origin`/`Host` 同源
- 错误：huma 默认的 RFC 9457 problem+json：`{ "title", "status", "detail", "errors": [{ "message", "location", "value" }] }`；业务附加数据放在扩展字段里（见各接口）

## 公共类型

```ts
type Issue = {
  level: "error" | "warning";
  source: "yaml" | "schema" | "semantic" | "validate" | "inventory"; // validate = bin/validate
  message: string;
  path?: string;        // 如 "all.children.pg-meta.vars.pg_users[0].name"
  line?: number;        // 1-based，能定位时返回
  column?: number;
};

type CommitInfo = {
  hash: string;         // git commit sha
  shortHash: string;
  author: string;       // console 用户名，或 "external"
  message: string;
  time: string;         // RFC 3339
};
```

## 健康与元信息

`GET /api/v1/health`（无需登录）→ `{ status: "ok", version: string }`

`GET /api/v1/meta`（需登录）→
```ts
{
  version: string;            // console 版本
  pigstyVersion: string;      // 从 pigsty.yml all.vars.version 读取，读不到为 ""
  pigstyHome: string;
  inventoryEngine: "ansible" | "builtin"; // 本机无 ansible-inventory 时为 builtin
  validateAvailable: boolean; // bin/validate 是否可执行
}
```

## 认证

- `POST /api/v1/auth/login` ← `{ username, password }` → `{ username }`，设置 cookie；失败 401
- `POST /api/v1/auth/logout` → 204
- `GET  /api/v1/auth/me` → `{ username }`；未登录 401
- `PUT  /api/v1/auth/password` ← `{ oldPassword, newPassword }` → 204

## 配置

`GET /api/v1/config` →
```ts
{ text: string; hash: string /* sha256 hex of text */; commit: CommitInfo | null }
```

`PUT /api/v1/config` ← `{ text: string; baseHash: string; message: string }`
- 200 → `{ hash: string; commit: CommitInfo }`
- 409 冲突 → problem 扩展字段 `{ currentText: string; currentHash: string }`
- 422 校验失败 → problem 扩展字段 `{ issues: Issue[] }`（只有 level=error 的问题会阻止保存；warning 一并返回但不阻止）
- 文本未变化 → 200，返回当前 hash 与 commit，不产生新 commit

`POST /api/v1/config/validate` ← `{ text: string }` → `{ ok: boolean; issues: Issue[] }`
（运行 bin/validate 与 inventory 解析；纯只读）

`GET /api/v1/config/history?limit=50&offset=0` → `{ items: CommitInfo[]; total: number }`

`GET /api/v1/config/history/{hash}` → `{ commit: CommitInfo; text: string }`

`GET /api/v1/config/diff?from={hash}&to={hash|"HEAD"}` → `{ from: string; to: string; fromText: string; toText: string; unified: string }`

`POST /api/v1/config/revert` ← `{ hash: string; baseHash: string; message?: string }` → 同 PUT /config 的 200 响应；冲突/校验失败同上

外部修改：后端 fsnotify 检测到 pigsty.yml 被 UI 以外修改时自动提交，author = "external"。前端通过轮询 `GET /config` 的 hash（30 秒）发现变化。

## 有效值

`GET /api/v1/inventory/hosts` →
```ts
{ hosts: { ip: string; groups: string[] }[]; groups: { name: string; hosts: string[]; module: string | null }[] }
```
`module` 由身份参数推断：PGSQL / INFRA / ETCD / MINIO / REDIS / APP / null。

`GET /api/v1/inventory/effective?host={ip}` →
```ts
{
  host: string;
  engine: "ansible" | "builtin";
  vars: {
    key: string;
    value: unknown;
    source: "default" | "global" | "group" | "host";
    sourceGroup?: string;   // source=group 时的组名
    sensitive: boolean;
  }[];
}
```
`default` 来源的值来自参数 schema 中的默认值。

## 参数 Schema

`GET /api/v1/schema` → `schema/v<pigstyVersion>.json` 的内容（找不到对应版本时回退到最新一份），结构见 DESIGN.md §5.2：
```ts
{
  version: string;
  modules: string[];
  params: Record<string, {
    module: string; section?: string; role: string;
    level: "GLOBAL" | "CLUSTER" | "INSTANCE" | "NODE" | string;
    type: "string" | "integer" | "number" | "boolean" | "array" | "object" | "any";
    default?: unknown; enum?: unknown[];
    identity?: boolean; sensitive?: boolean;
    impact?: string;        // none | reload | restart | playbook | init-only | action:<id>
    desc?: string;          // 上游英文注释
    label?: string;         // 中文名（可选）
    doc?: string;
    items?: unknown; $ref?: string;
  }>;
  defs: Record<string, unknown>;
}
```
