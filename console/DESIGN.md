# Pigsty Console 设计文档（MVP 定稿）

> 状态：方案定稿，未开始编码 · 适用 Pigsty v4.5.0 · License Apache-2.0
> 目标：一个可视化编辑 `pigsty.yml` 并完成「编辑 → 校验 → 保存 → 执行 → 观测」闭环的轻量控制台，团队内部使用，同时开源给 Pigsty 社区。

---

## 0. 已确认的决策

| 项 | 决策 |
|---|---|
| 管理范围 | 1 整套 Pigsty：数个 PG 集群、十数个 PG 实例、十数到数十个 node |
| 部署位置 | 管理节点（`admin_ip`）上，复用其 ansible / SSH 免密 / `pigsty.yml` |
| 节点侧 | 无 Agent，console 通过 ansible（SSH）与 HTTP（只读）触达各 node |
| 配置事实来源 | `pigsty.yml` 文件 + 独立 git 仓库版本化 |
| 后端 | Go 单二进制（内嵌前端），1c1g 可运行，空闲 RSS 目标 < 40 MB |
| 前端 | React 19 + TypeScript + Vite + **Ant Design 6** |
| 代码位置 | 本仓库 `console/` 目录，与上游代码隔离 |
| 账号 | 本地账号，所有登录用户同等权限；谁做了什么靠作业记录与配置 git 历史追溯，不单独做审计系统 |
| 侧重 | 小团队日常运维；不做大公司（RBAC/SSO/审批/多租户）与开源社区（多语言/多版本/通用发行）向的功能 |
| 界面语言 | 仅中文，文案集中在一个文件 |
| 危险操作 | MVP 含 switchover、重启实例、移除 replica（带闸门）；删除整集群/主库只给 CLI 提示 |
| MVP 范围 | 完整闭环（配置编辑 + 执行 + 状态/漂移）+ 内存占用预估（§12）+ 备份查看/恢复/回滚（§13） |

### MVP 功能清单

**① 配置管理**（§4–§7）
- 配置树：全局 / 组 / 主机三层浏览。
- 四种编辑视图：
  - 表单：常用参数。
  - YAML 源码：带补全和文档提示。
  - 有效值：显示最终值和来源。
  - 节点×模块矩阵。
- 专用编辑器：用户、数据库、HBA、服务、扩展。
- 新建集群向导：选 HA 形态，生成配置片段。
- 即时校验：语法、类型、语义、交叉引用、默认密码等安全提示。
- 保存：语义 diff 确认后写入 git；可查看历史、比较任意两个版本、回滚。
- 检测并记录在 UI 之外的手工修改。

**② 执行变更**（§8–§10）
- 根据配置变化自动给出「待应用动作」，一键生成作业。
- 低风险动作：用户、数据库、HBA、服务、扩展、crontab。
- 中风险动作：纳管节点、新建集群、加 replica、改 Patroni 参数。
- 高风险动作（带闸门）：switchover、重启实例、移除 replica。
- 作业：逐主机进度、实时日志、取消；console 重启后作业不中断。

**③ 状态与漂移**（§11）
- 概览：集群和节点健康、告警、未应用变更、运行中作业。
- 集群详情：成员实时角色、复制延迟，Grafana 深链。
- 漂移检测：failover 后主从和配置不一致时提示，并可一键回写配置。

**④ 内存预估**（§12）
- 编辑配置时实时显示每个节点的内存堆叠条，超配时标红。
- `/capacity` 容量页：所有节点汇总，可展开公式明细，可调节假设。
- 已运行集群：预估值和实际值对比，包括参数实际值和内存实际占用。

**⑤ 备份与恢复**（§13）
- 备份状态：最近一次备份、RPO、归档失败、仓库大小。
- 备份时间线：可恢复窗口、备份链、恢复点、时间线切换。
- 备份操作：手动备份、备份检查、创建恢复点；高风险作业执行前自动打恢复点。
- PITR 向导：恢复到新集群或原地恢复；默认先暂停，验证后再提升；收尾时补一次全量备份。

**⑥ 基础**
- 本地账号登录（账号用命令行管理），敏感字段打码。
- Go 单二进制，部署在管理节点，经 Pigsty Nginx 用 HTTPS 访问，1c1g 可运行。

### 明确不做

| 类别 | 不做的功能 |
|---|---|
| 大公司向 | 角色权限/RBAC、SSO/OIDC、操作审批与双人复核、独立审计系统、多租户、多套 Pigsty 统一管理（Hub） |
| 社区向 | 多语言、同时支持多个 Pigsty 版本、通用安装包与发行渠道、多架构全覆盖、上游对齐工作 |
| 运维高危 | UI 里删除整个集群或主库、移除节点、删除 infra/etcd/minio、大版本升级（只给 CLI 提示） |
| 其他 | 自建监控图表（统一跳转 Grafana）、在 UI 里执行业务 SQL、批量跨集群编排 |

---

## 1. 架构

```
浏览器 ──HTTPS──> Pigsty Nginx（infra_portal: console → 127.0.0.1:<port>）
                         │
          ┌──────────────▼──────────── 管理节点 ────────────────────────┐
          │ pigsty-console（Go，运行用户 = 安装 Pigsty 的管理用户）        │
          │  configstore : pigsty.yml 读写 + 独立 git + 乐观锁 + fsnotify │
          │  inventory   : ansible-inventory --list（有效值计算）          │
          │  validate    : bin/validate + inventory 解析                   │
          │  actions     : 白名单动作目录 → 命令模板                       │
          │  jobs        : systemd-run 托管 ansible-playbook，NDJSON + SSE │
          │  status      : VictoriaMetrics / Patroni / pgbackrest / AM     │
          │  store       : SQLite（用户、会话、作业、已应用版本）        │
          └──────┬──────────────────────┬──────────────────────────────────┘
                 │ SSH (ansible)         │ HTTP 只读
             各 node               Patroni :8008 / VM / Alertmanager
```

设计原则：

1. **后端薄、前端厚**：YAML 结构化编辑、语义 diff、建议动作推导都在前端（TS）完成；后端只存文本、做权威校验、执行白名单动作。
2. **复用 Pigsty/Ansible 自身逻辑**：有效值用 `ansible-inventory`，校验用 `bin/validate`，执行用原生 playbook，不重新实现。
3. **不依赖自己管理的数据库**：console 自身状态存 SQLite，PG 故障时 console 仍可用。
4. **没有任意 shell 入口**：后端只执行动作目录里声明的命令模板，所有 playbook 强制带 `-l`。
5. **不读业务数据**：只访问 `pg_stat_*`/`pg_catalog`/监控指标，不提供执行 SQL 的功能。

---

## 2. 技术栈

### 后端（Go 当前稳定版 1.26+）

| 用途 | 选型 | 说明 |
|---|---|---|
| HTTP + OpenAPI | `github.com/danielgtaylor/huma/v2`（底层 stdlib `net/http`） | 由 Go 结构体生成 OpenAPI 3.1，前端据此生成类型 |
| SQLite | `modernc.org/sqlite` | 纯 Go、无 CGO，便于静态编译与交叉编译 |
| 迁移 | `github.com/pressly/goose/v3`（embed SQL） | |
| YAML 读取 | `go.yaml.in/yaml/v3`（官方维护分支） | 后端只读不写结构 |
| git | 调用系统 `git` CLI | 比 go-git 省内存、行为可预期 |
| 文件监听 | `github.com/fsnotify/fsnotify` | 检测 UI 外的手工修改 |
| 密码哈希 | `golang.org/x/crypto/argon2`（argon2id） | |
| 日志 | stdlib `log/slog` | |
| 实时推送 | SSE（stdlib 实现） | 作业日志、状态刷新 |

### 前端

| 用途 | 选型 |
|---|---|
| 框架 | React 19 + TypeScript + Vite |
| 组件库 | Ant Design 6（+ `@ant-design/icons`） |
| 路由/数据 | TanStack Router + TanStack Query |
| API 客户端 | `openapi-typescript` + `openapi-fetch`（由后端 OpenAPI 生成） |
| YAML | `yaml`（eemeli/yaml，Document API 保留注释/格式） |
| 源码编辑 | Monaco + `monaco-yaml`（懒加载） |
| 表单校验 | Antd Form + `zod`（zod schema 由参数 Schema 生成） |
| diff 展示 | Monaco DiffEditor |
| 图表 | ECharts 6（`echarts-for-react`）：备份时间线（自定义系列）、内存堆叠条 |
| 测试 | Vitest（单元）+ Playwright（E2E） |

---

## 3. 目录布局

```
console/
├── DESIGN.md                    # 本文档
├── README.md                    # 中文：安装、使用、安全须知
├── go.mod
├── cmd/pigsty-console/          # 子命令：serve / init / user add|passwd / version
├── internal/
│   ├── api/                     # huma 路由与 handler
│   ├── auth/                    # 本地账号、会话 cookie
│   ├── configstore/             # 文件读写、git、hash 乐观锁、fsnotify
│   ├── inventory/               # ansible-inventory 调用与缓存
│   ├── validate/                # 调 bin/validate 并结构化结果
│   ├── actions/                 # 动作目录加载、参数校验、命令拼装
│   ├── jobs/                    # systemd-run、日志落盘、NDJSON 解析、SSE
│   ├── status/                  # VM / Patroni / pgbackrest / Alertmanager 采集
│   └── store/                   # SQLite + migrations
├── actions/catalog.yaml         # 动作目录（embed）
├── ansible/callback_plugins/
│   └── pigsty_events.py         # 输出 NDJSON 事件
├── schema/
│   ├── v4.5.0.json              # 生成产物（提交入库）
│   ├── overrides.yaml           # 人工补充：枚举、复杂对象、影响等级
│   └── memory-baseline.yaml     # 各组件内存经验值（待校准）
├── tools/schemagen/             # 扫描 roles/*/defaults/main.yml 生成 schema
├── web/
│   └── src/
│       ├── routes/              # 页面
│       ├── features/{config,clusters,jobs,status,backup,capacity}/
│       ├── yaml/                # Document 操作封装（读/改/定位/保真）
│       ├── rules/               # 语义校验规则（移植 bin/validate）
│       ├── planner/             # 语义 diff → 建议动作
│       ├── memory/              # 内存预估模型（按 Pigsty 版本）
│       ├── schema/              # 参数 schema 加载、zod 生成
│       └── texts.ts             # 界面文案集中管理
└── packaging/
    ├── pigsty-console.service   # systemd unit
    ├── portal.example.yml       # infra_portal 片段
    └── install.sh
```

对仓库已有文件的改动仅限：`Makefile`（新增 `console`、`console-dev` 目标）、`.github/workflows/console.yml`、`.gitignore`（`console/web/dist`、`node_modules`）。

> 注意：根目录 `.gitignore` 忽略了所有名为 `docs/` 的目录，console 的文档不要放在 `console/docs/` 下。

---

## 4. 配置存储与版本化

### 4.1 独立 git 仓库

`~/pigsty` 本身常是 pigsty 源码 checkout（`pigsty.yml` 被上游跟踪），因此**不能**提交到该仓库。使用独立 git 目录：

```bash
git --git-dir=/var/lib/pigsty-console/config.git --work-tree=$PIGSTY_HOME add pigsty.yml
```

- 只跟踪 `pigsty.yml`（后续可扩展自定义模板文件）。
- `pigsty-console init` 时初始化仓库，并把当前文件作为首个 commit。
- UI 保存 = 一次 commit，`author` = 登录用户，message = 用户填写 + 自动生成的语义摘要。
- fsnotify 检测到外部修改 → 延迟 2 秒去抖后自动提交 `external change (detected by console)`，作者标记为 `external`。

### 4.2 保存流程与冲突

```
前端 PUT /config {text, baseHash, message}
  ├─ baseHash ≠ 当前文件 sha256 → 409，返回当前文本，前端展示「磁盘版本 vs 我的草稿」diff，用户在磁盘版本基础上重新应用修改（草稿保留）
  ├─ 写入临时文件 → 跑权威校验（§6.2）→ 失败则 422 返回 issues
  ├─ 原子 rename 覆盖 pigsty.yml
  └─ git commit → 返回 {hash, commit}
```

- 前端草稿存 `localStorage`（键：baseHash），刷新不丢。
- 回滚 = 把历史版本文本作为新内容走同一保存流程（产生新 commit，不改历史）。

### 4.3 YAML 保真

- 前端用 `yaml` 的 `parseDocument` 得到 Document，所有表单修改通过 `doc.setIn(path, value)` 等 API 完成，再 `doc.toString()` 输出。
- 新增节点时继承同级节点的 flow 风格（如 hosts 下的 `{ pg_seq: 1, pg_role: primary }` 单行风格）。
- **验收测试（golden）**：对 `conf/**/*.yml` 与 `pigsty.yml` 逐个：解析 → 原样输出必须字节一致；解析 → 改一个标量 → 输出的 diff 只能出现在该行。

---

## 5. 参数 Schema

### 5.1 来源

`tools/schemagen` 扫描 `roles/*/defaults/main.yml`，解析两类行：

```yaml
pg_mode: pgsql          #CLUSTER  # pgsql cluster mode: pgsql,citus,...
# pg_cluster:           #CLUSTER  # pgsql cluster name, required identity parameter
```

- 提取：名称、默认值（注释行则无默认值）、层级（`#GLOBAL/#CLUSTER/#INSTANCE`/`#NODE` 等）、描述、所属 role、所属段落（`# PG_IDENTITY` 这类分隔注释）。
- 类型按默认值推断（bool/int/string/list/dict），无法推断的进 `overrides.yaml`。
- 再与 `overrides.yaml` 合并，输出 `schema/v<version>.json`。

### 5.2 输出结构（示例）

```json
{
  "version": "v4.5.0",
  "modules": ["INFRA", "NODE", "ETCD", "MINIO", "PGSQL", "REDIS", "DOCKER", "APP"],
  "params": {
    "pg_mode": {
      "module": "PGSQL", "section": "PG_IDENTITY", "role": "pgsql",
      "level": "CLUSTER", "type": "string", "default": "pgsql",
      "enum": ["pgsql","citus","mssql","mysql","ivory","pgtde","polar","gpsql","agens","oriole","pgedge"],
      "identity": false, "sensitive": false, "impact": "init-only",
      "desc": "pgsql cluster mode", "label": "PG 集群模式",
      "doc": "https://pigsty.io/docs/pgsql/param#pg_mode"
    },
    "pg_admin_password": {
      "module": "PGSQL", "level": "GLOBAL", "type": "string",
      "sensitive": true, "impact": "playbook", "default": "DBUser.DBA"
    },
    "pg_users": { "type": "array", "items": { "$ref": "#/defs/pg_user" }, "level": "CLUSTER", "impact": "action:pgsql-user" }
  },
  "defs": {
    "pg_user": {
      "type": "object", "required": ["name"],
      "properties": {
        "name": {"type": "string"}, "password": {"type": "string", "sensitive": true},
        "login": {"type": "boolean"}, "superuser": {"type": "boolean"},
        "roles": {"type": "array", "items": {"type": "string"}},
        "pgbouncer": {"type": "boolean"}, "comment": {"type": "string"},
        "state": {"enum": ["create", "absent"]}
      }
    }
  }
}
```

- `desc` 直接取上游注释（英文）；`label` 中文名只为表单里常用的参数在 `overrides.yaml` 中补充，不翻译全部参数。
- `impact` 取值：`none`（仅元数据）、`reload`、`restart`、`playbook`、`init-only`（只在初始化时生效）、`action:<id>`（对应动作目录）。
- 复杂对象定义（`defs`）至少覆盖：`pg_user`、`pg_database`、`hba_rule`、`pg_service`、`pg_instance`、`pgbackrest_repo`、`infra_portal_entry`、`app`、`minio_user`、`redis_instance`。字段以文档 `pgsql/config/{user,db,hba}.md` 为准，schemagen 只生成骨架，人工校对。
- CI：每次运行 schemagen 并 `git diff --exit-code schema/`，上游参数变化会让 CI 失败，提醒更新 overrides。

### 5.3 模块识别（身份参数）

| 身份参数 | 模块 |
|---|---|
| `pg_cluster` | PGSQL |
| `infra_seq`（组 `infra`） | INFRA |
| `etcd_cluster` | ETCD |
| `minio_cluster` | MINIO |
| `redis_cluster` | REDIS |
| `docker_enabled` / `apps` | DOCKER / APP |
| 所有 hosts | NODE |

---

## 6. 校验

### 6.1 前端即时校验（TS，`web/src/rules`）

- L1 语法：`yaml` 解析错误，定位到行列。
- L2 Schema：类型、枚举、必填、未知参数（警告，可能是拼写错误）。
- L3 语义（移植 `bin/validate`）：集群内 `pg_seq` 唯一且为正整数、恰好一个 primary、IP 格式、etcd 节点数为奇数（警告）、VIP 在网段内且不与节点 IP 冲突、hba 规则字段合法、service 定义合法。
- L4 交叉引用：数据库 `owner` 在 `pg_users` 中存在、用户 `roles` 引用合法、`pgbackrest_method` 对应 `pgbackrest_repo` 键存在、同一 IP 在多个组中的同名参数冲突（警告）。
- 安全基线（警告）：使用默认密码（与 schema default 相同）、`node_firewall_public_port` 含 5432、`pg_safeguard: false`。

### 6.2 后端权威校验（保存前）

1. 写入临时文件。
2. `python3 bin/validate <tmp>` → 解析输出为 issues。
3. `ansible-inventory -i <tmp> --list` → 能成功解析即通过，并顺便刷新有效值缓存。

任一失败返回 422；前端规则与后端规则共用同一组测试用例（`console/testdata/validate/*.yml`），保证不一致时能被发现。

---

## 7. 有效值视图

- 后端执行 `ansible-inventory -i pigsty.yml --list`，按文件 hash 缓存结果（变更时刷新，约 1–2 秒）。
- 对每个 host 输出：最终值 + 来源（`default` / `global` / `group:<name>` / `host`）。来源的判定：Ansible 只给最终值，来源由后端对照原始 YAML 的 `all.vars`、各组 `vars`、`hosts.<ip>` 推断（按 host → group → global → default 的顺序查找第一个定义处）。
- 多组同名变量冲突时（同一 IP 属于多个组），按 Ansible 规则（组深度、字母序）标出胜出方，并给警告。

---

## 8. 动作目录（白名单）

### 8.1 定义格式（`actions/catalog.yaml`）

```yaml
- id: pgsql-user
  title: { zh: 创建/更新业务用户, en: Create/Update User }
  risk: low
  check: true                      # 是否支持 --check 预览
  params:
    cluster: { type: pg_cluster }  # 必须是 inventory 中存在的 PG 集群
    user:    { type: pg_user_of, of: cluster }   # 必须在该集群 pg_users 中定义
  steps:
    - playbook: pgsql-user.yml
      limit: "{{cluster}}"
      extra_vars: { username: "{{user}}" }

- id: pgsql-add-replica
  risk: medium
  check: false
  params:
    cluster: { type: pg_cluster }
    ips:     { type: ip_list_in_cluster, of: cluster, min: 1 }
  preconditions: [ nodes_initialized ]       # 节点已执行过 node.yml
  steps:
    - playbook: pgsql.yml
      limit: "{{ips | join(',')}}"
    - playbook: pgsql.yml
      limit: "{{cluster}}"
      tags: [pg_service]
```

后端处理流程：加载目录 → 校验参数类型（对照当前 inventory）→ 检查前置条件 → 渲染命令 → 断言每条 `ansible-playbook` 都有 `-l` → 提交作业。前端只能提交 `{action, params, check, confirmName}`，不能提交命令。

### 8.2 MVP 动作清单

命令均取自对应 `bin/*` 脚本的实际调用（`bin/` 脚本仅作参考，console 直接调用 playbook，便于控制参数与输出）。标注「待沙箱核实」的需在 M3/M4 实测。

| id | 风险 | 命令 | check | 备注 |
|---|---|---|---|---|
| `pgsql-user` | 低 | `./pgsql-user.yml -l <cls> -e username=<user>` | 是 | 用户须先在 `pg_users` 定义；`state: absent` 时升为高风险 |
| `pgsql-db` | 低 | `./pgsql-db.yml -l <cls> -e dbname=<db>` | 是 | 同上，`state: absent` 升为高风险 |
| `pgsql-hba` | 低 | `./pgsql.yml -l <cls\|ips> -t pg_hba,pg_reload,pgbouncer_hba,pgbouncer_reload` | 是 | |
| `pgsql-svc` | 低 | `./pgsql.yml -l <cls\|ips> -t pg_service` | 是 | |
| `pgsql-ext` | 低 | `./pgsql.yml -l <cls> -t pg_ext [-e pg_extensions=[...]]` | 否 | 仅安装软件包；`CREATE EXTENSION` 走 `pgsql-db` |
| `pgsql-crontab` | 低 | `./pgsql.yml -l <cls> -t pg_crontab` | 是 | 待沙箱核实 |
| `node-add` | 中 | `./node.yml -l <ips>` | 否 | 新节点纳管 |
| `pgsql-add-replica` | 中 | `./pgsql.yml -l <ips>` → `./pgsql.yml -l <cls> -t pg_service` | 否 | 见 8.1 |
| `pgsql-create` | 中 | `./pgsql.yml -l <cls>` | 否 | 仅允许对**尚未初始化**的集群（Patroni 无成员）执行 |
| `patroni-param` | 中 | 在集群任一成员以 dbsu 执行 `patronictl -c /pg/bin/patroni.yml edit-config <cls> -s <k>=<v> --force` | 否 | 待沙箱核实；需展示参数是否要重启 |
| `pg-restart` | 高 | `patronictl ... restart <cls> <member> --force` | 否 | 闸门；待沙箱核实 |
| `pg-switchover` | 高 | `patronictl ... switchover <cls> --leader <a> --candidate <b> --force` | 否 | 闸门；成功后提示回写 `pg_role` |
| `pgsql-rm-replica` | 高 | `./pgsql-rm.yml -l <ips>` → `./pgsql.yml -l <cls> -t pg_service` | 否 | 闸门；**目标必须是 replica/offline**，主库拒绝 |
| `backup-info` | 只读 | ad-hoc：主库 dbsu 执行 `pgbackrest info --output=json` | — | 状态采集用 |

不进 MVP（UI 只展示 CLI 命令与文档链接）：删除整集群（`pgsql-rm.yml -l <cls>`）、删除主库、`node-rm`、`infra-rm`/`etcd-rm`/`minio-rm`、大版本升级。备份与 PITR 动作见 §13.4。

### 8.3 高风险闸门

1. 检查最近一次成功备份时间（`backup-info`），默认超过 26 小时则阻断（阈值可配置）。
2. 展示影响面：目标实例、当前角色、复制延迟、将执行的完整命令。
3. 用户手动输入目标名称（如 `pg-test-3` 或 IP）。
4. 执行；确认人与输入内容记入作业记录。

---

## 9. 作业执行

### 9.1 运行方式

```bash
systemd-run --user --unit=pigsty-job-<id> --collect \
  --working-directory=$PIGSTY_HOME \
  --setenv=ANSIBLE_CALLBACK_PLUGINS=<console>/ansible/callback_plugins \
  --setenv=ANSIBLE_CALLBACKS_ENABLED=pigsty_events \
  --setenv=PIGSTY_EVENTS_FILE=/var/lib/pigsty-console/jobs/<id>/events.ndjson \
  --setenv=ANSIBLE_FORCE_COLOR=0 --setenv=ANSIBLE_FORKS=<n> \
  sh -c 'ansible-playbook ... > /var/lib/pigsty-console/jobs/<id>/stdout.log 2>&1'
```

- 作业由 systemd 托管，console 重启/升级不会中断它；重启后通过 `systemctl --user show` 查询状态并接续 tail 日志。
- `--user` 模式需要对运行用户执行 `loginctl enable-linger`（安装脚本完成）；不可用时退化为 `setsid` + PID 文件。
- `ANSIBLE_FORKS` 默认 10（同 `ansible.cfg`），1c1g 机器建议 5，可配置。
- 一个动作的多个 step 顺序执行，任一 step 失败即停止。

### 9.2 事件格式（callback 插件输出）

```json
{"ts":"...","type":"playbook_start","playbook":"pgsql.yml"}
{"ts":"...","type":"task_start","play":"...","task":"pg_hba : render hba","task_id":"..."}
{"ts":"...","type":"runner_result","host":"10.10.10.11","task_id":"...","status":"changed","duration_ms":312}
{"ts":"...","type":"runner_result","host":"10.10.10.12","task_id":"...","status":"failed","msg":"..."}
{"ts":"...","type":"stats","hosts":{"10.10.10.11":{"ok":20,"changed":3,"failed":0,"unreachable":0}}}
```

`msg` 中可能包含密码，插件对 `no_log` 任务不输出 result，后端再按敏感参数值做一次脱敏后才推给前端。

### 9.3 并发与取消

- 全局同时只允许 1 个变更作业（只读采集不受限）。
- 取消：向 unit 发送 SIGINT，10 秒未退出则发 SIGTERM；UI 提示中途中断可能导致状态不一致。
- 作业记录：动作 id、参数、渲染后的命令、配置 commit、发起人、开始/结束时间、退出码、各主机统计。

---

## 10. Plan → Apply：已应用版本

- 表 `applied(cluster, action, commit, job_id, at)`：某集群某动作最近一次成功执行时的配置 commit。
- 集群的「最近应用版本」= 该集群所有成功作业中最新的 commit；集群从未通过 console 执行过时，以 `init` 时的首个 commit 作为基线。
- 前端对「基线 commit 的集群配置」与「HEAD 的集群配置」做语义 diff，由 `planner` 规则表推导建议动作：

| 语义变化 | 建议动作 |
|---|---|
| `pg_users[name=x]` 新增/修改 | `pgsql-user {cluster, user:x}` |
| `pg_databases[name=x]` 新增/修改 | `pgsql-db {cluster, db:x}` |
| `pg_hba_rules` / `pgb_hba_rules` 变化 | `pgsql-hba {cluster}` |
| `pg_services` / `pg_default_services` 变化 | `pgsql-svc {cluster}` |
| `pg_extensions` 变化 | `pgsql-ext {cluster}` |
| `pg_crontab` 变化 | `pgsql-crontab {cluster}` |
| `hosts` 新增 IP | `node-add {ips}` → `pgsql-add-replica {cluster, ips}` |
| `hosts` 移除 IP | 提示：配置已删除，需执行 `pgsql-rm-replica`（必须在删除配置**之前**执行，否则 inventory 中已没有该主机）|
| `pg_parameters` 变化 | `patroni-param`（逐项） |
| `impact: init-only` 的参数变化 | 警告：对已有集群不会生效 |
| 其他 | 提示「无对应动作，请参考文档手动处理」 |

> 关于移除实例：由于 playbook 依赖 inventory，正确顺序是「先执行移除动作 → 再从配置中删除主机」。UI 中「移除实例」按钮从集群详情页发起，执行成功后自动生成删除该主机的配置修改并保存。

---

## 11. 状态采集与漂移

| 数据 | 来源 | 频率 |
|---|---|---|
| 节点存活、CPU/内存/磁盘 | VictoriaMetrics PromQL（`up`、`node_*`） | 30 秒 |
| PG 存活、连接、TPS、复制延迟 | VictoriaMetrics（`pg_up`、`pg_*` 指标） | 30 秒 |
| 集群成员、角色、时间线、延迟 | Patroni REST `GET http://<ip>:8008/cluster` | 页面打开时 15 秒 |
| 备份 | `backup-info` 动作 | 10 分钟 / 手动 |
| 告警 | Alertmanager `GET /api/v2/alerts` | 30 秒 |

- VictoriaMetrics、Alertmanager 的地址从 inventory 推导（infra 组节点 + 默认端口），可在设置中覆盖。具体端口以 `docs/ref/port` 为准，开发时核实。
- Patroni REST 若开启 TLS，使用 `files/pki/ca.crt` 校验证书。
- **漂移检测**：
  1. inventory 中 `pg_role: primary` 与 Patroni leader 不一致 → 提供「回写 pg_role」，生成一次配置修改（主库改为 primary、原主库改为 replica）。
  2. inventory 中的实例在 Patroni/指标中不存在或已下线 → 警告。
  3. Patroni 中存在、inventory 中没有的成员 → 警告。
- Grafana 深链：集群页、实例页、节点页链接到 Pigsty 自带 dashboard（`PGSQL Cluster`、`PGSQL Instance`、`NODE Instance` 等），dashboard uid 与变量名开发时从 `files/grafana` 中核实。

---

## 12. 内存占用预估

目标：在**改配置的同时**就能看到每个节点的内存会被谁占用、会不会超；对已运行的集群，把「配置推导值」和「实际运行值」放在一起对比。

### 12.1 Pigsty 实际怎么算内存参数（来自 `roles/pgsql/templates/{oltp,olap,crit,tiny}.yml`）

| 变量 | 公式 | oltp | olap | crit | tiny |
|---|---|---|---|---|---|
| `max_connections` | `pg_max_conn` ≥ 20 时取它，否则按模板默认 | 1000（服务指向 pgbouncer 时 500） | 500 | 同 oltp | 250 |
| `shared_buffers` | `ceil(node_mem_mb × pg_shared_buffer_ratio)`（默认 0.25） | ✓ | ✓ | ✓ | ✓ |
| `maintenance_work_mem` | `shared_buffers × k` | k=0.25 | k=0.50 | k=0.25 | k=0.25 |
| `work_mem` | `clamp(floor(shared_buffers / max_connections), 下限, 上限)` | 64–1024 MB | 64–8192 MB | 64–1024 MB | 16–256 MB |
| `effective_cache_size` | `node_mem_mb − shared_buffers`（只是提示值，不实际分配） | | | | |
| 固定值 | `wal_buffers: 16MB`、`hash_mem_multiplier: 8.0`、`huge_pages: try`、`autovacuum_max_workers` 3（tiny 为 2） | | | | |

`node_mem_mb` 由 `roles/node_id` 根据内存页数计算；`node_cpu` 决定 worker 数。

**两个关键事实**（会直接体现在 UI 的警告里）：

1. **这些参数写在 Patroni 的 `bootstrap.dcs.postgresql.parameters` 下**，只在集群**首次初始化**时由当时的主库按**它自己的内存**算一次，写进 DCS，之后全集群共用。因此：
   - 集群初始化后再改 `pg_shared_buffer_ratio`、`pg_conf`、`pg_max_conn`，**不会生效**，要用 `patroni-param` 动作改。
   - 同一集群里内存比主库小的 replica，会拿到和主库一样大的 `shared_buffers`，有 OOM 风险。
2. `shared_buffers` 按**整台机器的内存**来算，不扣除同机部署的其他组件。单机全家桶（INFRA + PG + ETCD + MINIO + Redis/Docker）或一机多实例（`pg_instances`）时很容易超配。

### 12.2 预估模型

对每个节点，汇总这台机器上所有模块的内存：

| 层 | 组成 | 来源 |
|---|---|---|
| **PG 实例**（每实例） | 常驻：`shared_buffers` + `wal_buffers` + 后台进程<br>连接：活跃连接数 × 单连接开销<br>查询：活跃连接数 × `work_mem`（典型）/ × `work_mem × hash_mem_multiplier`（上界）<br>维护：`autovacuum_max_workers × maintenance_work_mem` | 公式（12.1）+ 可调假设 |
| PG 周边 | patroni、pgbouncer、pg_exporter、pgbackrest（备份时） | 经验值表 |
| NODE | node_exporter、vector、haproxy、vip-manager、chronyd | 经验值表 |
| ETCD / MINIO | etcd、minio 进程 | 经验值表 |
| INFRA | VictoriaMetrics、VictoriaLogs、Grafana、Alertmanager、Nginx、blackbox_exporter、console 自身 | 经验值表 |
| REDIS | `redis_max_memory` × 实例数 × 系数（碎片 + BGSAVE 写时复制，默认 1.3） | 配置 + 系数 |
| DOCKER/APP | 用户手填，或读取实际 `docker stats` | 手填 / 实测 |
| 系统预留 | 内核、页缓存余量（默认 5% 且不少于 512 MB） | 可调 |
| 大页 | `node_hugepage_count × 2MB` 或 `node_hugepage_ratio × 内存`，这部分只有 PG 能用 | 配置 |

- **经验值表**放在 `schema/memory-baseline.yaml`，按 Pigsty 版本维护，可在设置里覆盖。初始数值**全部标注为待校准**，M4 在沙箱里实测后回填（见 12.4）。
- **可调假设**（在页面上直接调）：活跃连接数（默认 `min(max_connections, 4 × CPU 核数)`）、单连接开销（默认 10 MB）、Redis 系数、系统预留。
- **输出三个数**：
  - 常驻：一定会占用的部分。
  - 典型：按假设的活跃连接数计算。
  - 理论上界：所有连接都在跑哈希类大查询，只用来提示风险，不作为判断依据。
- **判定**：
  - 典型 > 90%，或常驻 > 75%：红色。
  - 典型 > 75%：黄色。
  - 开启了大页但 `shared_buffers` 放不进大页：警告。
  - 大页预留后剩余内存放不下其他组件：警告。
  - 同集群成员内存不一致：提示 12.1 的第 1 条。

### 12.3 计算放在哪里

- **前端即时计算**（TS，`web/src/memory/v4.5.0/`）：把 12.1 的公式移植过来，按版本打包。改配置时侧边栏实时更新，无需请求后端。
- **公式同步保障**：CI 用 Python + jinja2 直接渲染各模板开头的 `{% set %}` 段和注释摘要行（`# buffer, cache, maintain = ...`），对一组「内存 × CPU × 模板 × max_conn」组合和 TS 结果逐项比对。上游改了公式，CI 会失败。
- **权威预览**（只对已纳管的节点）：动作 `pg-config-preview`，执行 `./pgsql.yml -l <ip> -t pg_config --check --diff`，取 Ansible 真实渲染出的 patroni 配置摘要行，和前端估算对比。需在沙箱核实该 tag 在 check 模式下可用。
- **节点规格**：
  - 已纳管节点：从 VictoriaMetrics 取 `node_memory_MemTotal_bytes` 和 CPU 数。
  - 未纳管的新节点：在向导里手填规格，存在 SQLite 的 `node_spec(ip, cpu, mem_mb, disk_gb, source)` 表，不写进 `pigsty.yml`。
  - 可以拖动「假设内存」滑块，用来给新机器选规格。

### 12.4 实际值对比（已运行的集群）

| 实际数据 | 获取方式 |
|---|---|
| 集群实际生效的 PG 参数 | Patroni `GET :8008/config`（DCS 中的 `postgresql.parameters`），加上 `pg_settings` 系统视图（只读，非业务数据） |
| 节点实际内存使用 | VictoriaMetrics：`node_memory_MemAvailable_bytes` 等 |
| 各服务实际内存 | 只读 ad-hoc：`systemctl show <svc> -p MemoryCurrent`（patroni、postgres、pgbouncer、etcd、vector、grafana……），10 分钟一次 |

页面上每个组件同时显示「预估 / 实际」。差异大于 30% 时标注，并提示：
- 配置与实际参数不一致（多半是用 `edit-config` 改过参数，或初始化后才改了配置）。
- 经验值需要校准：一键把实测值写回本地的经验值覆盖。

### 12.5 交互

- **配置工作台右侧面板**：当前选中节点或集群的内存堆叠条。改 `pg_conf`、`pg_shared_buffer_ratio`、`pg_max_conn`、`redis_max_memory`，新增实例或模块时实时变化，并给出与修改前的差值。
- **`/capacity` 容量页**：所有节点一张表，列出总内存、常驻、典型、上界、状态。点击展开公式明细，例如：「`shared_buffers` = 16384 MB × 0.25（来源 `pg_shared_buffer_ratio`，global）= 4096 MB」。
- **保存校验**：保存时若有节点变红，作为警告级 issue 出现在保存弹窗里，不阻止保存。

---

## 13. 备份查看、恢复与回滚

### 13.1 三种「回滚」要分清

| 类型 | 回滚对象 | 实现 | 风险 |
|---|---|---|---|
| 配置回滚 | `pigsty.yml` | git 历史（§4），回滚后可能需要重新执行动作 | 低 |
| 变更回滚 | 某个作业做的改动 | 一般无法自动逆向；按回滚后的配置重新执行对应动作 | 视动作而定 |
| **数据回滚** | 数据库内容 | **PITR**：恢复到某个时间点、恢复点、LSN 或 xid | 高 |

### 13.2 Pigsty 的备份机制（已核对）

- pgBackRest：`pgbackrest_method` 选择 `pgbackrest_repo` 里的一个仓库。默认 `local`（主库本机 `/pg/backup`，保留 2 个全量）；也支持 `minio`/S3（可加密，按天数保留）。
- 初始化后自动做一次全量备份（`pgbackrest_init_backup: true`）。定时备份靠 `pg_crontab`，例如 `00 01 * * * /pg/bin/pg-backup full`。
- PITR 由 `pgsql-pitr.yml -l <集群> -e '{"pg_pitr": {...}}'` 执行。`pg_pitr` 支持：
  - `cluster`：源集群
  - `type`：default/time/xid/name/lsn/immediate
  - `time`、`name`、`xid`、`lsn`
  - `set`：备份集
  - `timeline`、`exclusive`
  - `action`：pause/promote/shutdown，**默认 pause**
  - `archive`：探索性恢复时设为 false
  - `db_include`/`db_exclude`、`process`、`repo`
  - `backup`：恢复前把原数据拷到 `/pg/data-backup`
  - `data`、`port`
- playbook 分三个阶段：
  - `down`：暂停 HA，停 patroni 和 postgres
  - `pitr`：config → backup → restore → recovery → verify
  - `up`：清理 etcd 元数据 → 启动主库和从库 → 恢复 HA

### 13.3 备份查看

**数据来源**：

- 主库以 dbsu 身份只读执行 `pgbackrest --stanza=<cls> info --output=json`，结果缓存 10 分钟，备份或恢复作业结束后立即刷新。stanza 名默认等于 `pg_cluster`，待核实。
- WAL 归档状态来自 `pg_stat_archiver`（系统视图）或 pg_exporter 的对应指标，指标名待核实。
- 恢复点和时间线切换，从作业记录和 `pgbackrest info` 的 archive 段里获取。

**集群「备份」tab 的内容**：

1. **状态卡片**：
   - 仓库类型、位置、保留策略、是否加密（只显示加密算法，不显示口令）。
   - 最近一次全量、增量备份的时间，并按阈值着色。
   - RPO 指标：当前时间减去最后一次归档 WAL 的时间。
   - 归档失败次数和最近一次失败的时间。
   - 仓库总大小。
2. **时间线图**（ECharts 自定义系列）：
   - 横轴是时间。全量、差异、增量备份用不同高度的标记表示，并画出备份之间的依赖链。
   - **可恢复窗口**用阴影带表示：从最早备份的结束点，到最新归档的 WAL。
   - 恢复点用小旗标出，PG 时间线切换（failover 或 PITR）用竖线标出。
   - console 执行过的高风险作业用图标叠加显示。
   - 在图上**点击某个时间点，即可作为 PITR 目标**，自动带入恢复向导。
3. **备份列表**：标签、类型、开始/结束时间、耗时、数据库大小、备份大小（压缩后）、增量大小、LSN 范围、WAL 范围、依赖的父备份，以及按保留策略预计会被淘汰的时间。
4. **概览页汇总**：所有集群的备份新鲜度与 RPO 排行。

### 13.4 备份相关动作（加入动作目录）

| id | 风险 | 命令 | 说明 |
|---|---|---|---|
| `backup-info` | 只读 | `pgbackrest --stanza=<cls> info --output=json`（主库，dbsu） | 已在 §8.2 中 |
| `backup-run` | 低 | 在主库以 dbsu 执行 `/pg/bin/pg-backup <full\|diff\|incr>` | 同一集群同时只能有一个备份；路径待核实 |
| `backup-check` | 只读 | `pgbackrest --stanza=<cls> check` | 检查归档与仓库连通性 |
| `restore-point` | 低 | 在主库执行 `SELECT pg_create_restore_point('<name>')` | 只调用系统函数，不触及业务数据 |
| `pitr-clone` | 中 | 先 `pgsql-create` 一个新集群，再执行 `./pgsql-pitr.yml -l <新集群> -e '{"pg_pitr":{"cluster":"<源>", ...}}'` | 不影响源集群；**仓库必须能被新节点访问**（见 13.5） |
| `pitr-inplace` | **最高** | `./pgsql-pitr.yml -l <cls> -e '{"pg_pitr":{...}}'` | 覆盖当前集群；最严格的闸门（见 13.6） |
| `pitr-promote` | 高 | 在 `action: pause` 恢复并验证完成后，提升主库并恢复 HA | 具体命令待沙箱核实（`pg_ctl promote` / `pg_wal_replay_resume()` + `pgsql-pitr.yml -t up`） |

**自动恢复点**：每次执行**高风险作业**和 `state: absent` 的用户/数据库动作之前，console 先在目标集群主库上创建恢复点 `console_job_<id>`，并记录它的 LSN 和时间。作业详情页会有一个「回滚到此作业执行前」按钮，直接带着该恢复点打开 PITR 向导。

### 13.5 PITR 向导

**① 选目标**
- 恢复目标可以是：时间线图上点选的时间点、某个恢复点、某个备份集（immediate）、最新（latest），或高级选项里的 LSN/xid。
- 时间**同时显示 UTC 和本地时区**；提交时统一转换成 `pg_pitr.time` 要求的 `YYYY-MM-DD HH:MM:SS+00` 格式，避免时区搞错。
- 校验目标是否在可恢复窗口内，并显示将使用哪个备份集（目标之前最近的一个）、需要回放的 WAL 范围，以及预计耗时（备份大小 ÷ 经验吞吐量，标注为估算）。

**② 选方式**
- **恢复到新集群（推荐，默认）**：
  - 向导生成新集群的配置片段，默认名为 `<源集群>-pitr`、`archive: false`。用户选择节点后，进入配置工作台确认并保存。
  - 然后依次执行 `pgsql-create` 和 `pitr-clone`。
  - **限制**：当 `pgbackrest_method: local` 时，备份在源主库的本地磁盘上，其他节点读不到，因此只能恢复到源主库所在节点的另一个端口（`pg_pitr.port`/`data`，待核实），或改用 MinIO/S3 仓库。向导会自动识别并说明。
- **原地恢复**：覆盖当前集群，必须走最高级闸门（见 13.6），默认勾选 `backup: true`。

**③ 选恢复后动作**
- 默认 `pause`：恢复到目标后暂停，等用户验证。
- 可选 `promote`：直接提升。

**④ 执行**
- 按 `down → pitr → up` 三个阶段展示进度，并逐主机展示 task。

**⑤ 验证（action = pause 时）**
- 页面给出只读连接信息和检查建议，例如核对关键表的行数和最近写入时间。这一步由用户自己连接验证，console 不读业务数据。
- 验证通过后点「确认提升」，执行 `pitr-promote`；也可以「调整目标重新恢复」。

**⑥ 收尾**
- 提示并一键发起一次全量备份，为新时间线建立基线。
- 原地恢复的情况下，提示 `/pg/data-backup` 占用的磁盘，确认无误后由用户手动清理。

### 13.6 原地 PITR 的闸门

在 §8.3 的基础上再加几条：

1. 明确展示**将丢失的数据区间**：从恢复目标到现在。
2. 勾选 `backup: true` 时，检查数据盘剩余空间是否 ≥ 当前数据目录大小（来自节点指标），不够就阻断。
3. 用户输入集群名称，**并**勾选「我已确认业务已停写或已通知相关方」。
4. 恢复期间这个集群的其他动作全部锁定；概览页显示醒目的「恢复中」横幅。

---

## 14. 账号与安全

- 账号用命令行管理：`pigsty-console user add|passwd|del <name>`；UI 里只能改自己的密码。
- 所有账号同等权限；作业记录发起人，配置 commit 记录作者，足够小团队追溯。
- 会话：HttpOnly + Secure + SameSite=Strict cookie，空闲 12 小时过期；连续登录失败做简单延时。
- 写接口校验 `Origin`，防 CSRF。
- 监听 `127.0.0.1`，经 Pigsty Nginx 用 HTTPS 暴露（`infra_portal` 增加一项）。
- 敏感参数（schema 中 `sensitive: true`，以及名字匹配 `password|secret|key|pass` 的字段）在表单、有效值视图、diff 中默认打码，点击显示。
- 不读取、不传输 `files/pki/ca/ca.key`。
- 不做：角色权限、SSO、ansible-vault 加密字段、操作审批。

---

## 15. 页面

| 路由 | 页面 | 主要内容 |
|---|---|---|
| `/` | 概览 | 集群/节点健康卡片、未应用变更、运行中作业、最近备份、告警、漂移提醒 |
| `/config` | 配置工作台 | 左：全局/组/主机树；中：表单 / YAML / 有效值 / 节点×模块矩阵；右：校验与文档；保存弹窗（语义 diff + 行级 diff + 提交说明） |
| `/config/history` | 配置历史 | commit 列表、任意两版本 diff、回滚 |
| `/clusters` | 集群列表 | 每个集群的模块类型、成员数、健康、未应用变更角标 |
| `/clusters/:cls` | 集群详情 | 成员拓扑（实时角色/延迟）、用户、数据库、HBA、服务、扩展、备份、待应用变更（一键生成作业）、Grafana 链接、高风险操作入口 |
| `/clusters/new` | 新建集群向导 | 选 HA 形态 → 选节点 → 用户/库 → 生成配置片段 → 进入工作台确认 → 执行 |
| `/nodes` | 节点列表 | IP、所属组、模块、存活、资源、纳管状态 |
| `/jobs`、`/jobs/:id` | 作业 | 列表；详情：主机 × task 进度矩阵、完整日志（SSE）、取消 |
| `/clusters/:cls/backup` | 备份 | 状态卡片、时间线、备份列表、手动备份、恢复点 |
| `/clusters/:cls/pitr` | PITR 向导 | 选目标 → 选方式 → 恢复后动作 → 执行 → 验证 → 收尾 |
| `/capacity` | 容量 | 全部节点内存预估与实际对比、公式明细、假设调节 |
| `/settings` | 设置 | 改自己的密码、外部地址、备份阈值、forks |

---

## 16. API（`/api/v1`）

```
POST   /auth/login            POST /auth/logout          GET /auth/me
GET    /config                → {text, hash, commit}
PUT    /config                ← {text, baseHash, message}        409 冲突 / 422 校验失败
POST   /config/validate       ← {text} → {issues[]}
GET    /config/history        GET /config/history/{commit}
GET    /config/diff?from=&to=
POST   /config/revert         ← {commit, baseHash, message}
GET    /inventory/effective?host=|group=  → [{key, value, source}]
GET    /schema                → 当前版本参数 schema
GET    /actions               → 动作目录（参数 schema、风险、check 支持）
POST   /actions/{id}/plan     ← {params} → {commands[], preconditions[], risk}
POST   /jobs                  ← {action, params, check, confirmName?}
GET    /jobs  GET /jobs/{id}  GET /jobs/{id}/events (SSE)  POST /jobs/{id}/cancel
GET    /status/overview  GET /status/clusters  GET /status/clusters/{cls}
GET    /status/nodes     GET /status/drift     GET /status/backups  GET /status/alerts
GET    /applied           → 各集群最近应用版本
GET    /status/clusters/{cls}/pgconfig   → Patroni DCS 参数 + pg_settings（实际值）
GET    /status/nodes/{ip}/memory         → 总内存、已用、各服务 MemoryCurrent
GET/PUT /node-specs/{ip}                 → 未纳管节点的规划规格
GET    /schema/memory-baseline           → 经验值表（含本地覆盖）
GET    /backups/{cls}                    → pgbackrest info + 归档状态 + 恢复点 + 时间线事件
POST   /backups/{cls}/pitr/plan          ← pg_pitr 参数 → {备份集, WAL 范围, 丢失区间, 预计耗时, 前置检查}
PUT    /auth/password
```

---

## 17. 里程碑（1–2 人，约 14–16 周）

| 里程碑 | 周期 | 交付 | 验收 |
|---|---|---|---|
| M0 骨架 | 1 周 | 目录与脚手架、Go 内嵌前端、`init`/`user add`、登录、SQLite 迁移、CI、systemd unit、portal 示例、schemagen v1 | 1c1g 虚拟机空闲 RSS < 40 MB；单二进制 < 40 MB |
| M1 配置读写 | 2–3 周 | 配置树、核心参数表单、YAML 编辑器、有效值视图、前后端校验、保存/git/历史/diff/回滚、外部修改检测 | YAML 保真 golden 测试全部通过 |
| M2 复杂编辑器 + 内存预估 | 2–3 周 | users/dbs/hba/services/extensions 编辑器、节点×模块矩阵、新建集群向导、内存预估模型与侧边栏、`/capacity` 页、公式 CI 比对 | 能用 UI 从零配出与 `conf/ha/trio.yml` 等价的配置；TS 公式与 jinja2 渲染在测试矩阵上完全一致 |
| M3 执行闭环 | 2–3 周 | callback 插件、systemd-run 作业、SSE、低风险动作、已应用版本、建议动作 | 沙箱中经 UI 完成「新增用户/库/HBA → 执行 → 生效」 |
| M4 状态与中高风险 | 2–3 周 | 状态采集、集群详情、漂移检测与回写、内存实际值对比与经验值校准、node-add / 加 replica / switchover / 重启 / 移除 replica（闸门） | 沙箱手动 failover 后能检测漂移并回写；扩缩容 replica 全流程跑通 |
| M5 备份与恢复 | 2–3 周 | 备份 tab（状态、时间线、列表）、手动备份、检查、恢复点、自动恢复点、PITR 向导（新集群 / 原地）、验证与提升、收尾 | 沙箱中：误删表 → 用自动恢复点恢复到新集群验证 → 原地恢复 → 提升 → 全量备份，全流程跑通 |
| M6 收尾 | 1 周 | 中文 README、关键路径 E2E（配置保存、执行动作、PITR）、amd64 构建（需要再加 arm64）、安装脚本、安全自查 | 团队成员按 README 在新管理节点上装好并跑通一遍 |

---

## 18. 测试

- **单元**：YAML 保真 golden、语义规则（前后端共用 `testdata/validate`）、planner 规则、动作参数校验与命令渲染（断言必带 `-l`）、敏感值脱敏。
- **集成**：后端用真实 `git`、`ansible-inventory`、`bin/validate` 跑 handler 测试。
- **E2E**：仓库 `vagrant/` 沙箱（meta + 3 节点 pg-test），Playwright 驱动完整闭环；CI 中只跑不依赖虚拟机的部分，E2E 在本地/自托管 runner 上跑。
- **资源**：在 1c1g 虚拟机上压测空闲 RSS、一次 pgsql.yml 作业期间 console 自身内存与 CPU。

---

## 19. 风险与对策

| 风险 | 对策 |
|---|---|
| Pigsty 升级导致参数变化 | schema 按版本打包；CI 重新生成并对比 |
| YAML 往返丢注释/格式 | 前端用 eemeli/yaml；golden 测试兜底；不达标时降级为「生成片段 + 用户确认合并」 |
| 手工编辑与 UI 并发 | hash 乐观锁 + 冲突 diff + fsnotify 自动提交 |
| `--check` 覆盖不全 | 动作目录声明是否支持 check；主要靠限定范围 + 语义 diff + 确认 |
| 作业中断导致半完成 | systemd 托管不随 console 退出；取消前明确提示；依赖 playbook 幂等可重跑 |
| failover 后配置与实际不一致 | 漂移检测 + 一键回写 |
| console 权限过大 | 只监听本地、HTTPS、强制登录、动作白名单、作业与配置历史可追溯 |

---

## 20. 开源（后置）

- MVP 阶段以团队自用为准，不为社区做额外工作（多语言、多 Pigsty 版本、通用安装包、上游对齐）。
- 保持两点，方便以后开源：License 用 Apache-2.0；代码隔离在 `console/`，不改上游文件逻辑。
- README 写明安全模型：console 对整个集群有 root 等价权限，只应监听本地、经 HTTPS 与登录访问。

---

## 21. 待开发期核实的事项

- [ ] `patronictl` 在 Pigsty 中的配置路径与 `edit-config -s` / `restart` / `switchover` 的确切参数
- [ ] `pg_crontab` tag 单独执行是否幂等
- [ ] VictoriaMetrics、Alertmanager、Patroni 的默认端口与认证方式（对照端口表）
- [ ] Grafana dashboard uid 与变量名
- [ ] 各 playbook 在 `--check` 下是否能跑通（逐个动作验证后再在目录中标记 `check: true`）
- [ ] `systemd-run --user` 在 EL8/9/10、Debian 12/13、Ubuntu 22/24 上的可用性
- [ ] console 的监听端口（避开 Pigsty 已占用端口）
- [ ] `./pgsql.yml -t pg_config --check --diff` 能否作为渲染预览
- [ ] patroni.yml 中 `postgresql.parameters`（本地段）与 DCS 段各自包含哪些参数
- [ ] 各组件内存经验值（在沙箱实测后回填 `memory-baseline.yaml`）
- [ ] pgbackrest stanza 名是否等于 `pg_cluster`；`/pg/bin/pg-backup` 的路径与参数
- [ ] WAL 归档相关的 pg_exporter 指标名
- [ ] `action: pause` 后 playbook 的 `up` 阶段行为，以及提升主库的正确步骤
- [ ] 原地 PITR 时 replica 如何重建；`local` 仓库下能否用 `port`/`data` 在源节点另起实例做克隆恢复
