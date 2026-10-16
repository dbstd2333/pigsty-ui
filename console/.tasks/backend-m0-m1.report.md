# 后端任务报告：M0 + M1

- 范围：仅改动 `console/`（`console/web/` 只新增 `embed.go`，未改动上游任何文件，未 git commit）
- 状态：`go vet ./...`、`go test ./...` 全绿；端到端 curl 流程已跑通；`pigsty.yml` 已还原为原始字节（sha256 `eed3952a…3a5e`，`git status` 只有未跟踪的 `console/`）

---

## 1. 实现了什么

### 目录与依赖
- Go module：`github.com/dbstd2333/pigsty-ui/console`（`go 1.26`）
- 依赖：huma v2.39.1（stdlib `net/http` 适配器）、modernc.org/sqlite v1.60.1、goose v3.29.0（embed 迁移）、
  go.yaml.in/yaml/v3 v3.0.5、fsnotify v1.10.1、x/crypto v0.58.0、x/term v0.47.0、stdlib `log/slog`
- `cmd/pigsty-console`：`serve` / `init` / `user add|passwd|del` / `version`（子命令支持 flag 与位置参数混排）
- `internal/`：`api`、`auth`、`configstore`、`inventory`、`schema`、`store`、`validate`、`version`
- `tools/schemagen`、`schema/{overrides.yaml,v4.5.0.json,embed.go}`、`web/embed.go`、`packaging/*`、`Makefile`、`.gitignore`

### 配置读写（M1 核心）
- `GET /config` 直接读磁盘（text/hash/HEAD commit），不缓存、不常驻轮询
- `PUT /config`：baseHash 乐观锁 → 临时文件（同目录隐藏文件）→ 权威校验 → 原子 rename（保留原文件权限）→ git commit（作者=登录用户）
  - 文本未变化：不写、不提交，返回当前 hash 与 HEAD
  - 校验失败：不改文件、不提交，返回 422 + `issues`
  - 冲突：返回 409 + `{currentText,currentHash}`
- `GET /config/history`、`GET /config/history/{hash}`、`GET /config/diff`、`POST /config/revert`（回滚=新提交，默认消息 `revert to <short>`）
- git 一律调用系统 `git` CLI，`--git-dir=<data>/config.git --work-tree=<PIGSTY_HOME>`，只用 `add -- pigsty.yml`；
  额外加了 `-c commit.gpgsign=false -c core.hooksPath=/dev/null`，作者/提交者用 `GIT_AUTHOR_*`/`GIT_COMMITTER_*` 环境变量
- fsnotify 监听 `PIGSTY_HOME` 目录（只认 `pigsty.yml`），去抖 2s 后提交，author=`external`，消息 `external change (detected by console)`；
  console 自己写入的内容（seenHash 命中）不会产生 external 提交；启动时额外做一次 `SyncExternal`，把停机期间的手工修改补记一笔

### 有效值与校验
- 有效值：优先 `ansible-inventory -i <file> --list`（用 `_meta.hostvars` 的权威值 + 回查 YAML 定位来源）；无 ansible 时退化为 builtin 引擎
  （自己解析 YAML，按 host → group → global → schema default 计算，组间冲突按 Ansible 的 `sort_groups (depth, priority, name)` 规则取最后一个定义并标出 `sourceGroup`）
- 解析出的模型按文件 hash 缓存（无后台轮询），`${admin_ip}` 之类占位符原样返回
- 校验：`python3 <home>/bin/validate <tmp>` 输出解析成 Issue（`[WARN]`→warning、`[ERRO]/[ERROR]`→error、`[FAIL]` 兜底），
  并从消息里的引号路径提取 `Issue.path`；再叠加 inventory 结构解析（带 YAML 行列号）；有 ansible 时还会跑一次 `ansible-inventory`
- 敏感字段：schema `sensitive: true` 或键名匹配 `password|secret|_key|pass`（在有效值响应里返回 `sensitive: true`，由前端打码）

### 认证与前端嵌入
- argon2id（`m=19MiB,t=2,p=1`，OWASP 交互式推荐值）；哈希后调用 `debug.FreeOSMemory()` 归还工作内存；并发哈希限 2 路
- 会话存 SQLite（token 只存 sha256），cookie `pc_session`：HttpOnly + SameSite=Strict + Path=/ + Max-Age 12h；
  `Secure` 默认按监听地址判断（非回环=开），`--cookie-secure` 可显式覆盖；空闲 12h 过期，1 分钟粒度续期
- 登录失败按账号累计延时（200ms×次数，上限 2s），未知账号走同等开销的占位 hash，避免用户名枚举
- 写请求校验 Origin/Host 同源（无 Origin 的非浏览器客户端放行），`--dev-origin` 默认 `http://localhost:5173`
- 前端：`web/embed.go`（构建标签 `embedui`，`//go:embed all:dist`）；不带标签时非 `/api` 路径返回中文开发提示页；
  带标签时 SPA 回退（未知路径→index.html），`/api` 优先于 SPA

### schemagen
- 扫描 `roles/*/defaults/main.yml`，解析 `key: value #LEVEL # desc`、`# key: …`、`# [INSTANCE]`/`# [OPTIONAL]`、块状 list/dict 默认值、引号内的 `#`
- 段落标题取 `# PG_IDENTITY` 这类分隔块；带 `(Reference)` 的段落标记为参考定义
- 同名参数合并：优先「模块主 role + 非 *_remove + 非 Reference」的定义，其次按 role 名序（保留首个），`role` 记为胜出 role
- 与 `schema/overrides.yaml` 合并（提供 `pg_user`/`pg_database`/`hba_rule`/`pg_service`/`pgbackrest_repo`/`infra_portal_entry`/`infra_service_entry`/`app` 等 defs、
  `pg_conf`/`pg_mode`/`pgbackrest_method`/`node_tune`/`pg_role` 等 enum、身份参数类型、`app`/`apps` 等角色里没有的参数）
- 产物 `schema/v4.5.0.json`：441 参数 / 8 defs / 11 模块，重复生成字节一致（`shasum` 相同）

### Makefile / 打包
- `make build`（`pnpm -C web build` + `go build -tags embedui`）、`build-api`、`dev-api`、`schema`、`test`、`lint`
- `packaging/pigsty-console.service`（含 `GODEBUG=madvdontneed=1` 说明）、`packaging/portal.example.yml`（`c.pigsty` → `127.0.0.1:8860`）

---

## 2. 本地启动（开发）

```bash
cd console
# 1) 初始化数据目录 + SQLite + 独立 git 仓库（首个 commit）
go run ./cmd/pigsty-console init --pigsty-home .. --data-dir ./.data
# 2) 创建账号（密码从 stdin 或交互输入）
printf 'admin123\n' | go run ./cmd/pigsty-console user add admin --pigsty-home .. --data-dir ./.data
# 3) 启动后端（默认 127.0.0.1:8860）
go run ./cmd/pigsty-console serve --pigsty-home .. --data-dir ./.data
# 4) 前端开发服务器（代理 /api 到 8860）
pnpm -C web dev     # http://localhost:5173
# 5) 发布构建（内嵌前端单二进制）
make build          # 或：pnpm -C web build && go build -tags embedui -o pigsty-console ./cmd/pigsty-console
```

改密码 / 删账号：`pigsty-console user passwd <name>`、`pigsty-console user del <name>`（密码同样从 stdin 或 TTY 交互读取）。
`schema` 重新生成：`go run ./tools/schemagen --pigsty-home .. --out schema/v4.5.0.json`。

---

## 3. 验证结果

### 静态与测试
- `go vet ./...` 通过；`go test -count=1 ./...` 通过（api 0.6s / auth 1.3s / configstore 5.7s / inventory、schema、validate、schemagen < 0.1s）
- 单测覆盖：保存流程（成功、冲突、校验失败、无变化不提交、外部提交、fsnotify 去抖与「自己写的不算外部」、回滚、ref 注入防护）、
  builtin 有效值（host>group>global>default、组名冲突、ansible hostvars 路径、模块推断、加载错误）、
  schemagen 解析/owner/overrides、认证（argon2 往返、策略、会话与闲置过期、改密/删除）、validate 输出解析、schema 产物加载、API 契约（httptest）
- `make lint` / `make test` / `make build-api` / `make schema` 均可用；schemagen 两次生成 sha256 一致

### 端到端 curl 流程（`--pigsty-home .. --data-dir ./.data`，端口 8868，脚本 `/tmp/pc-final.sh`）
| 步骤 | 结果 |
|---|---|
| `GET /health` | 200 `{"status":"ok","version":"0.1.0"}` |
| 未登录 `GET /config` | 401 `application/problem+json` |
| `POST /auth/login` | 200 + `Set-Cookie: pc_session=…; HttpOnly; SameSite=Strict; Max-Age=43200`；错误密码 401（249ms） |
| `GET /config` | text 与磁盘字节一致、hash = sha256(text)、`commit` 为 HEAD |
| `GET /meta` | `inventoryEngine=builtin`、`validateAvailable=true`、`pigstyVersion=v4.5.0` |
| `POST /config/validate` | 200 `{"ok":true,"issues":[]}` |
| `PUT /config`（baseHash 错误） | 409 + `currentHash`/`currentText` |
| `PUT /config`（改一行注释） | 200，hash=sha256(new)，commit author=`admin`，磁盘已更新 |
| 文本未变化再 PUT | 200，history total 不变（不产生 commit） |
| `GET /config/history` | 200，total 与提交列表正确 |
| `GET /config/diff?from=<init>&to=HEAD` | 200，16 行 unified diff 命中注释改动 |
| `GET /inventory/hosts` | 200，`app/APP`、`etcd/ETCD`、`infra/INFRA`、`pg-meta/PGSQL` |
| `GET /inventory/effective?host=10.10.10.10` | 200，engine=builtin，361 个参数（37.6 KB），`pg_cluster` source=group pg-meta、`pg_admin_password` masked、`pg_version` source=default 被 global 覆盖 |
| `GET /schema` | 200，version v4.5.0 / 441 params / 8 defs |
| 手工改文件（fsnotify） | 约 3.5s 后自动提交，author=`external`，消息 `external change (detected by console)` |
| `POST /config/revert` | 200，回到初始内容（hash 与原始文件相同） |
| `POST /auth/logout` | 204，之后 `/auth/me` 401 |
| 收尾 | `pigsty.yml` sha256 与原始一致；`git -C .. status` 仅 `?? console/`；服务以退出码 0 停止 |
| 内嵌前端（`-tags embedui`） | `/` 与 `/config`、`/clusters/pg-meta` → 200 index.html；真实 `assets/*.js` → 200；`/api/v1/health` → JSON；`/api/v1/nope` → 404 problem+json |

### 常驻内存（`proc_pid_rusage`）
| 时点 | resident_size | phys_footprint |
|---|---|---|
| 启动后空闲 | 17.9 MiB | 7.3 MiB |
| 登录后 | 39.4 MiB | 24.5 MiB |
| 走完 history/diff/schema/effective 后空闲 | 40.2 MiB | 25.0 MiB |
| 完整 e2e 结束后 | 41.5 MiB | 30.0 MiB |

---

## 4. 与 API.md 的偏差

**字段名、状态码、错误扩展字段均未改动**，逐条对齐 API.md。以下为契约未覆盖、由实现补齐的细节：

1. 未知 `/api/…` 路径返回 `application/problem+json` 404（`{"title":"Not Found","status":404,"detail":"接口不存在"}`），不会掉进前端页面。
2. `/api/v1/health`、`/api/v1/auth/login` 免登录；`/api/v1/openapi.json`（以及 huma 附带的 `openapi.yaml` 等）同样免登录，OpenAPI 3.1。文档 UI 已关闭（未使用 `/docs`）。
3. huma 会先做请求体校验：`PUT /config` 的 `message` 按契约（无 `?`）视为必填；`POST /config/revert` 的 `message` 可选。
   这类 huma 校验失败的响应是 422 + problem `errors[]`，而业务校验失败（bin/validate / inventory / 结构）才是 422 + 扩展字段 `issues[]`（两者状态码相同，前端按 `issues` 是否存在判断）。
4. 409/422 的扩展字段按契约返回；其它错误用 huma 默认 problem+json（`errors` 字段在无明细时省略）。
5. `GET /config/history` 的 `limit` 上限 500（超出按 500 处理），`offset` 负数按 0 处理。
6. `CommitInfo.time` 为 RFC 3339（git 作者时间）。

## 5. 已知问题

1. **常驻内存指标临界**：启动空闲 17.9 MiB，登录后 ~40 MiB（resident）/25–30 MiB（footprint）。Go 的 scavenger 在 macOS 用 `MADV_FREE_REUSABLE`，
   归还的 argon2 工作内存仍计入 `resident_size`（已 `debug.FreeOSMemory()`，但 Go 不会立刻全部归还给内核）。生产是 Linux + systemd，
   单元文件里已加 `Environment=GODEBUG=madvdontneed=1`，使页面立即归还内核；**建议在真实 1c1g 虚拟机（Linux）上复测一次空闲 RSS**。
2. **ansible 路径未在真机验证**：本机没有 `ansible-inventory`，`/meta` 报 `builtin`，ansible 分支只覆盖了单测（`EffectiveFromHostVars`）。
   需在一台装了 ansible 的机器上复验 `inventoryEngine=ansible` 与 `_meta.hostvars` 的来源归属。
3. **外部修改与 UI 保存的竞争**：外部改动落在 2s 去抖窗口内、且随后发生一次 UI 保存时，外部状态不会单独留下一笔提交（只保留 UI 的提交，内容仍然正确）。
   另外，多个 console 实例或手工 `git` 操作会争抢 `config.git/index.lock`，失败时只记日志（`cannot commit external change`），下次文件变更会重试。
   目前没有对 index.lock 做重试；单实例部署不受影响。
4. **提交信息不做语义摘要**：按契约使用前端传入的 `message`（空则 `update pigsty.yml`）；语义摘要由前端在 `message` 里给出（DESIGN §4.1 的「人工消息 + 自动摘要」）。
   `init` 首个 commit 的 author 取当前 OS 用户，`external` 提交用固定 author。
5. **schemagen 归属**：极少数只出现在 `*_remove`/Reference 段落里的参数（如 `pg_safeguard`）`role` 记为 `pg_remove`；`app`/`apps`/`version` 等由 `overrides.yaml` 补全。
   `level` 仅在上游注释里出现 `#GLOBAL/#CLUSTER/#INSTANCE/#NODE`（或 `# [..]`）时才有值，其余为空字符串（由 overrides 逐项补）。
6. **`.data/` 已加入 `console/.gitignore`，未提交**；里面留有端到端测试产生的若干提交（一个本地数据仓库，不影响上游仓库）。
7. **遗留进程**：本轮早期启动的一个 serve 实例（PID 90371，端口 8860，旧构建）在沙箱内无法被杀掉（跨 exec 会话的 signal 被拒：`kill: operation not permitted`）。
   请在终端执行 `kill 90371`（或 `lsof -ti:8860 | xargs kill`）释放 8860，以免前端联调时连到旧进程。
