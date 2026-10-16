# 后端任务书：M0 + M1（交给 Codex）

你负责 Pigsty Console 的 **Go 后端**。前端由另一位工程师在 `console/web/` 并行开发，**不要创建或修改 `console/web/` 下的任何文件**（唯一例外见第 6 条 embed.go）。不要修改仓库中 `console/` 以外的任何文件。不要 git commit。

必读：
- `console/DESIGN.md`：整体设计（重点 §1、§2 后端栈、§3 目录、§4 配置存储、§5 Schema、§6 校验、§7 有效值、§14 账号与安全）
- `console/API.md`：本阶段 API 契约，**必须严格按此实现**，字段名、状态码、错误扩展字段都不能变
- 仓库根目录即 PIGSTY_HOME：`pigsty.yml`、`bin/validate`、`roles/*/defaults/main.yml`、`conf/**/*.yml`

## 范围

1. Go module 位于 `console/`，module 路径 `github.com/dbstd2333/pigsty-ui/console`，Go 1.26。
2. 依赖按 DESIGN §2：huma v2（stdlib net/http 适配器）、modernc.org/sqlite、goose v3（embed 迁移）、go.yaml.in/yaml/v3、fsnotify、x/crypto/argon2、log/slog。不要引入 Web 框架或 ORM。
3. `cmd/pigsty-console`：子命令
   - `serve [--listen 127.0.0.1:8860] [--pigsty-home <dir>] [--data-dir <dir>]`
   - `init`：初始化数据目录、SQLite、独立 git 仓库（`--git-dir=<data>/config.git --work-tree=<pigsty-home>`，只跟踪 pigsty.yml），首个 commit
   - `user add|passwd|del <name>`（密码从 stdin 或交互读取）
   - `version`
   - 默认值：pigsty-home = 当前目录；data-dir = `/var/lib/pigsty-console`，环境变量 `PIGSTY_CONSOLE_DATA` 可覆盖。开发时用 `--pigsty-home .. --data-dir ./.data`（在 console 目录下运行）。
4. `internal/`：api、auth、configstore、inventory、validate、schema、store 等包，按 DESIGN §3。
5. 实现 API.md 中的全部接口，以及 `GET /api/v1/openapi.json`。
   - 保存：base hash 乐观锁 → 写临时文件 → 校验（bin/validate + inventory 解析；level=error 阻止保存）→ 原子 rename → git commit（作者 = 登录用户）。文本未变不产生 commit。
   - fsnotify 监听 pigsty.yml，去抖 2 秒后对外部修改自动提交，author = `external`。注意不要把 console 自己的写入当作外部修改。
   - git 一律调用系统 `git` CLI，用上面的 `--git-dir/--work-tree` 方式，绝不触碰仓库自身的 `.git`。
   - 有效值：优先调用 `ansible-inventory -i <file> --list`；**本机（macOS 开发机）没有 ansible**，此时退化为 builtin 引擎：自己解析 YAML，按 host → group → global → schema default 计算，并在响应里标 `engine: "builtin"`。来源判定按 DESIGN §7。`${admin_ip}` 这类占位符原样返回即可。
   - bin/validate：用 `python3 <pigsty-home>/bin/validate <file>` 执行，把输出解析成 Issue（source=validate）；不可执行时跳过并在 /meta 中报告。
   - 敏感字段判定：schema 中 sensitive=true，或键名匹配 `password|secret|_key|pass`。
6. 前端嵌入：在 `console/web/embed.go` 中用构建标签 `embedui` 来 `//go:embed all:dist`；不带该标签时，非 `/api` 路径返回一个简单提示页（说明开发时请访问 Vite 开发服务器）。带标签时做 SPA 回退（未知路径返回 index.html）。这是你唯一可以在 `console/web/` 下创建的文件。
7. 认证：argon2id；会话存 SQLite，cookie `pc_session`（HttpOnly、SameSite=Strict；监听非 localhost 或经 HTTPS 时加 Secure，可用 `--cookie-secure` 显式控制）；空闲 12 小时过期；连续失败登录简单延时。写请求校验 Origin 同源（开发时允许 `http://localhost:5173`，用 `--dev-origin` 配置）。
8. Schema 生成器 `tools/schemagen`（Go，`go run ./tools/schemagen --pigsty-home .. --out schema/v4.5.0.json`）：
   - 扫描 `roles/*/defaults/main.yml`，解析 `key: value  #LEVEL  # desc` 及被注释掉的 `# key:  #LEVEL # desc` 行；段落标题来自 `# PG_IDENTITY` 这类分隔注释
   - 按默认值推断类型；与 `schema/overrides.yaml` 合并（你先写一份覆盖常见复杂对象的初版：pg_users、pg_databases、pg_hba_rules、pg_services、pgbackrest_repo、infra_portal 的 defs，以及若干 enum，如 pg_conf、pg_mode、pgbackrest_method、node_tune）
   - 输出并提交生成结果 `schema/v4.5.0.json`；同名参数在多个 role 中出现时合并（保留首个定义，role 字段记录首个）
9. `Makefile`（放在 `console/Makefile`，不要改根 Makefile）：`build`（含前端：`pnpm -C web build` 后 `go build -tags embedui`）、`build-api`（仅后端）、`dev-api`、`schema`、`test`、`lint`（go vet）。
10. `packaging/pigsty-console.service`、`packaging/portal.example.yml`（infra_portal 片段，域名 `c.pigsty`，endpoint `127.0.0.1:8860`）。

## 质量要求

- 单元测试：保存流程（冲突、校验失败、无变化、成功提交）、builtin 有效值计算、schemagen 解析、认证。测试用临时目录，不要动真实 pigsty.yml。
- `go vet ./...`、`go test ./...` 全部通过。
- 空闲内存目标 < 40MB；不要常驻轮询任何东西（fsnotify 除外）。
- 代码风格：简洁、包内聚，注释适度。

## 完成后

1. 在 `console/.tasks/backend-m0-m1.report.md` 写一份简短报告：实现了什么、如何本地启动（含 init 与创建用户的命令）、与 API.md 的任何偏差（原则上不应有）、已知问题。
2. 在 console 目录下实际跑一遍：`go run ./cmd/pigsty-console init --pigsty-home .. --data-dir ./.data`，创建用户 `admin`/`admin123`，启动 serve，用 curl 走通登录、GET /config、validate、PUT /config（可改一处注释再改回）、history、diff、effective，把结果摘要写进报告。验证完停止服务。`.data/` 不要提交（在 `console/.gitignore` 中忽略）。
