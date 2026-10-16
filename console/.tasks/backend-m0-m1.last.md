后端任务已完成。报告写在 `console/.tasks/backend-m0-m1.report.md`，下面是结论摘要。

## 交付内容（全部在 `console/` 内）

- **Go module** `github.com/dbstd2333/pigsty-ui/console`（go 1.26）：huma v2 + humago（stdlib mux）、modernc sqlite、goose（embed 迁移）、yaml/v3、fsnotify、argon2、x/term、slog，无 Web 框架/ORM
- `cmd/pigsty-console`：`serve` / `init` / `user add|passwd|del` / `version`
- `internal/`：`api`（全部 API.md 接口 + `/api/v1/openapi.json`）、`auth`、`configstore`、`inventory`、`schema`、`store`、`validate`
- `tools/schemagen` + `schema/{overrides.yaml,v4.5.0.json}`：441 参数 / 8 defs / 11 模块，两次生成字节一致
- `web/embed.go`（唯一新增的 web 文件，构建标签 `embedui`）、`Makefile`、`.gitignore`、`packaging/{service,portal.example.yml}`

关键行为：保存=baseHash 乐观锁 → 同目录临时文件 → `bin/validate` + inventory 解析 → 原子 rename（保留权限）→ git CLI（`--git-dir/.data/config.git --work-tree=PIGSTY_HOME`，作者=登录用户）；未变化不提交；fsnotify 去抖 2s 提交 `external`（自己的写入不会误判）；无 ansible 时 builtin 引擎（host→group→global→default，组冲突按 Ansible `sort_groups` 规则）；session 存 SQLite、cookie `pc_session` 12h 空闲过期、同源写校验、失败登录递增延时。

## 验证结果（已跑，证据在报告里）

- `go vet ./...`、`go test -count=1 ./...` 全绿（api/auth/configstore/inventory/schema/validate/schemagen 均有测试）
- 端到端 curl（`--pigsty-home .. --data-dir ./.data`）：health / 401 / 登录（含失败 401+249ms）/ meta=builtin / validate ok / PUT 409（`currentText`+`currentHash`）/ PUT 200（author=admin，hash=sha256(new)）/ 未变化不产生 commit / history / diff(16 行命中注释改动) / effective(engine=builtin，361 参数，`pg_admin_password` 已标 sensitive) / schema / 手工改文件 3.5s 后 `external` 自动提交 / revert 回到初始内容 / logout 204
- **`pigsty.yml` 已还原为原始字节**（`eed3952a…3a5e`），`git -C .. status` 只有未跟踪的 `console/`，未 git commit
- `-tags embedui` 构建（36 MB）实测：`/`、`/config`、`/clusters/pg-meta` → index.html，真实 assets → 200，`/api/*` 优先，未知 API → problem+json 404；非 embedui 构建返回中文开发提示页
- 常驻内存：启动空闲 17.9 MiB / footprint 7.3 MiB；跑完全流程后 41.5 MiB / 30.0 MiB

## 需要注意的三件事

1. **内存指标临界**：登录后 ~40 MiB（Go 在 macOS/Linux 用 MADV_FREE，归还的 argon2 页面仍计入 RSS）。已把 argon2 降到 OWASP 推荐值（m=19MiB,t=2,p=1）+ 并发限流 + `FreeOSMemory`，并在 systemd 单元加了 `GODEBUG=madvdontneed=1`；建议在真实 Linux 1c1g 上复测空闲 RSS。
2. **ansible 引擎未真机验证**（本机无 `ansible-inventory`，`/meta` 报 builtin），该分支只有单测覆盖。
3. **遗留进程需你手动清理**：本轮早期启动的一个旧构建实例（PID 90371，占 127.0.0.1:8860）在沙箱内无法发信号（`kill: operation not permitted`），请执行 `kill 90371`（或 `lsof -ti:8860 | xargs kill`），否则前端联调会连到旧进程。要按任务书启动请用 `cd console && go run ./cmd/pigsty-console init --pigsty-home .. --data-dir ./.data` → `printf 'admin123\n' | go run ./cmd/pigsty-console user add admin --pigsty-home .. --data-dir ./.data` → `serve`。

与 API.md 无字段/状态码/扩展字段偏差；huma 请求体校验（如 `PUT /config` 的 `message` 必填）和未知 `/api` 路径的 problem+json 404 属契约未覆盖的补充细节，已在报告第 4 节逐条列出。