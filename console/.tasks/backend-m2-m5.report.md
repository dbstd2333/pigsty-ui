# 后端任务报告：M2–M5

- 范围：仅改动 `console/`（`console/web/` 一个字节都没动；仓库其他部分未改；未 git commit）
- 状态：`go vet ./...`、`go test ./...` 全绿；模拟模式端到端 curl 全流程已跑通（127.0.0.1:8861，验证后已停止）；
  `pigsty.yml` sha256 仍为 `eed3952a28c354970d49279d795475227b5f1115bcbb6acf9d3804df1f8d3a5e`（与 M0–M1 报告一致）
- 契约：`console/API-M2-M5.md` 的字段名、可空性、状态码、扩展字段、数组不返回 null 均逐条对齐；偏差见 §4

---

## 1. 实现了什么

### 新增包（`console/internal/`）

| 包 | 内容 |
|---|---|
| `plan` | 跨包共享的契约类型：`Risk`（含 `AtLeast/Upgrade`）、`Precondition`、`Gate`、`SourceInfo`、`RestorePoint`、pg_pitr 时间格式 `YYYY-MM-DD HH:MM:SS+00` 的解析/渲染 |
| `actions` | 嵌入的 `catalog.yaml`（20 条 MVP 动作）+ 参数校验 + 前置条件 + 命令渲染 + `-l` 断言 |
| `jobs` | 作业管理器：runner、callback 插件释放、事件总线与 tail、SSE、脱敏、并发锁、取消、模拟作业 |
| `status` | VictoriaMetrics / Patroni REST / Alertmanager 客户端、15s 缓存、漂移检测、按需 ad-hoc 内存采集、模拟状态 |
| `backup` | `pgbackrest info --output=json` 解析、可恢复窗口、PITR 评估、`PitrTarget → pg_pitr` 转换、模拟备份历史 |
| `settings` | 设置读写（SQLite）+ inventory 推导地址合并 |
| `services` | 装配层：`StatusSource`、`BackupFetcher`、`BackupStore`、`Facts`（实现 `actions.Facts`）、`AdHoc` |
| `ansible` | 嵌入并释放 `ansible/callback_plugins/pigsty_events.py` |

### 动作目录与命令安全（任务书 §1）

- `catalog.yaml` 与契约 MVP 表逐条一致（id/分类/风险/check/gate/参数/命令），20 条：`pgsql-create … pitr-promote`。
- 参数白名单：集群名/用户名/库名/恢复点名 `^[a-zA-Z0-9_.-]+$`（长度 ≤ 64），IP 必须是合法 IPv4，
  `pg_member`/`ip_list`（`of`）必须属于该集群，`pg_user`/`pg_db` 必须在 inventory 中定义，`enum`/`bool`/`int`/`object` 逐类型校验。
- 未知参数一律 422（唯一例外：模拟模式下允许 `fail: true`，见 §6.3）。
- 命令以 **argv 数组** 渲染并执行，绝不经 shell；`AssertArgv()` 在渲染期断言：
  每条 `./<playbook>` 必须有 `-l <非空且非 all>`；每条 `ansible` ad-hoc 的 argv[1] 必须是具体 IPv4。该断言被单测覆盖。
- 风险升级：`pg_users[].state=absent`、`pg_databases[].state∈{absent,recreate}` 升级为 high 并强制闸门。
- 前置条件（error 阻断 / warning 提示）：`cluster_absent`、`cluster_exists`、`source_exists`、`ips_in_cluster`、
  `not_primary`（inventory `pg_role` 与 Patroni leader 双重判断）、`member_exists`、`host_in_inventory`、
  `nodes_managed`、`backup_repo`、`pitr_window`、`not_degraded`；外部源不可达时降级为 warning，不误阻断。

### 作业执行（任务书 §2）

- 运行器：优先 `systemd-run --user --unit=pigsty-job-<id>-<n> --collect --working-directory=<home> --setenv=…`；
  探测不到用户级 systemd 时退化为「`exec-step` 子进程 + `Setpgid` 进程组 + PID/退出码文件」。
  本机没有 systemd（也没有 util-linux 的 `setsid`），因此实测走的是后者；两条路径都由同一个 `pigsty-console exec-step`
  helper 落地 stdout.log 与 `<step>.exit`，使「退出码」在两种模式下同源、且 console 重启后可判定。
- 事件：ansible callback 插件输出 ndjson（`playbook_start/play_start/task_start/runner_result/stats`，`no_log` 任务不出 result），
  后端 tail ndjson + stdout，统一编号为 `JobEvent`（seq 从 1 递增），落盘 `<data>/jobs/<id>/events.jsonl` 并经 SSE 推送；
  `state.json` 记录 step/pid/unit/偏移量，console 重启后 `Recover()` 重新接管运行中的作业（SQLite 只记 queued/running）。
- 脱敏：从当前 `pigsty.yml` 递归收集敏感键（`password|secret|_key|pass`，覆盖 `pg_users[].password`、`pg_databases[].password`、
  `minio_users[].secret_key`、`pgbackrest_repo.*.cipher_pass` 等）的实际值，输出前整体替换为 `***`（含 JSON 转义形态）；
  事件、作业错误、`/jobs/{id}/log` 全部经过同一脱敏器。产品词 `pigsty` 与 `postgres` 在白名单里，避免把路径/playbook 名打花。
- 并发与取消：全局 1 个变更作业（`risk=readonly` 的 `backup-check` 不占锁），冲突返回 409 + `runningJobId`；
  原地 PITR 运行期间该集群的变更作业 409。取消 = SIGINT，10 秒后 SIGTERM（systemd 用 `systemctl --user kill`，
  进程组模式用 `kill(-pid)`）；模拟模式取消直接闭合取消通道，立即结束。
- SQLite 新迁移 `00002_jobs.sql`：`jobs`、`applied`、`restore_points`、`node_specs`、`settings`（+ 索引）。
- 高风险（含 critical）作业执行前在主库创建 `console_job_<id>` 恢复点（ad-hoc `psql -c "SELECT pg_create_restore_point(...)"`），
  失败只记 warning 事件、不阻止作业；成功创建时立即写入 `restore_points` 表，并挂到 `Job.restorePoint`。
- 成功的非 check 作业更新 `applied`（cluster → 当前配置 commit，同时记 jobId/时间）；`restore-point` 动作成功后把恢复点写入表。

### 状态采集（任务书 §3）

- 地址推导：infra 组第一台主机 + inventory 中的 `vmetrics_port`(8428) / `alertmanager_port`(9059) / `grafana_port`(3000)，
  可被 `/settings` 覆盖；请求超时 4s。
- VictoriaMetrics 落点：`up{job="node"}`、`count by (ip, instance)(node_cpu_seconds_total{mode="idle"})`、
  `1-avg by (...)(rate(node_cpu_seconds_total{mode="idle"}[5m]))`、`node_load1`、`node_memory_MemTotal/Available_bytes`、
  `node_filesystem_size/avail_bytes`（优先 `/data` 挂载点）；`job="node"` 与 `ip` 标签取自 `roles/infra/templates/victoria/prometheus.yml`。
- Patroni：依次尝试集群成员 `:8008/cluster`、`/config`；`patroni_ssl_enabled` 时用 `<home>/files/pki/ca.crt` 校验证书
  （只读 ca.crt，绝不读 ca.key），REST 认证用 inventory 的 `patroni_username`/`patroni_password`。
- Alertmanager：`GET /api/v2/alerts`，只保留 active。
- 漂移：`role_mismatch`（inventory `pg_role` vs Patroni role，附 `fix{primary,replicas}`）、`missing_member`、`unknown_member`；
  注意 `pg_role`/`pg_seq` 一律从「该集群所属 group 的 host vars」读取（见 §4.9）。
- 内存页：ansible ad-hoc 只读 `systemctl show <units> -p Id -p MemoryCurrent`（19 个候选 unit，含 redis*），
  缓存 10 分钟，`?refresh=1` 强制刷新；`[not set]`/`infinite`/uint64 max 记为 null。所有取数都只在有请求时发生，无常驻轮询。

### 备份与 PITR（任务书 §4）

- `pgbackrest info --output=json` 解析（stanza 状态、repo type/path/cipher、backup 全集含 prior/LSN/WAL/大小、archive min/max）；
  单测使用与 pgBackRest 文档一致的样例 JSON（stanza 级 `backup[]` 为对象、`repo[].backup[]` 为 label/type 摘要）。
- 可恢复窗口 = 最早备份 stop → 最新归档时间（归档时间取自 pg_exporter 指标 `pg_archiver_finish_time`，按
  `job="pgsql", cls="<cluster>"` 匹配；指标名已对照 `roles/pg_exporters/templates/pg_exporter.yml` 核实）。
- PITR 评估：选备份集（目标之前最近的已成功备份）、`inWindow`、原地恢复丢失区间（目标 → 现在，name 目标用恢复点时间解析）、
  估算耗时（备份集大小 ÷ 150 MB/s，标注为估算）、local 仓库不可跨节点 clone、`backup:true` 时的磁盘空间检查
  （数据目录大小以「最新备份的 size」近似，取不到指标时降级为 warning）。
- `PitrTarget`（latest/time/name/lsn/xid/immediate + set/exclusive）→ `pg_pitr`：latest 不设 `type`，
  time 统一转 `YYYY-MM-DD HH:MM:SS+00`，clone 注入 `cluster=<源>`，inplace 注入 `backup:true`，再渲染成
  `./pgsql-pitr.yml -l <cls> -e '{"pg_pitr":{…}}'`（同一渲染器同时服务于 plan 接口与作业提交）。

### 设置、节点规格、已应用版本（任务书 §5）

- `GET/PUT /settings`（`victoriaUrl`/`alertmanagerUrl`/`grafanaUrl`/`backupMaxAgeHours` 默认 26/`ansibleForks` 默认 5/
  `memoryBaseline`，`derived` 只读），URL 必须 http(s)、数值范围校验。
- `GET/PUT/DELETE /node-specs`（不写 `pigsty.yml`）；`GET /applied`（集群取 PGSQL+REDIS，未执行过时为 init 首个 commit + `baseline:true`）。

### 模拟模式（任务书 §6）

`serve --simulate` 或 `PIGSTY_CONSOLE_SIMULATE=1`；`/meta` 返回 `simulate: true`。
- 作业：按动作与目标主机生成 play/task/runner_result/stats/log，task 间隔 0.35–1.5s，3–5 个 play × 3–5 个 task
  （`node-add`/`pgsql-create`/`pgsql-add-replica`/`pitr-*` 更长），实测单作业 13–20s；支持取消；参数含 `fail:true` 时最后一个 task 失败
  （runner_result=failed、退出码 2、作业 failed）。日志行同时写入 `stdout.log`，`/jobs/{id}/log` 可读。
- 状态：按 IP/集群名做 FNV 种子生成稳定数据（重启不变）；节点规格与指标齐全（managed=true）；
  PG 集群按 `pg_role` 生成 Patroni 成员（leader/replica + 少量 lag + 少量 pending_restart），
  **故意制造一个 role_mismatch 漂移**：首个 PG 集群把「第二个主机（或合成邻位 IP）」设为 leader，
  于是漂移页稳定出现 `role_mismatch`（单机集群额外出现 `unknown_member`）；另有 1 条 `PgExporterDown` 告警。
- 备份：每个 PG 集群生成近 7 天、每 3 天一个 full + 中间 incr 的历史，WAL 范围随时间长，
  含 archive 区间、`lastArchivedAt`、可恢复窗口；PITR 作业成功后其记录出现在 `events`。
- 恢复点：`restore-point` 动作写入 `restore_points`（含稳定合成的 LSN）；高风险作业的自动恢复点同样写表。
- 内存页：按主机+服务生成稳定数值。

### 其它

- `cmd/pigsty-console`：新增 `--simulate` 与内部子命令 `exec-step --exit-file <f> --log-file <f> -- <argv…>`
  （作业运行器调用；直接执行 argv，不经 shell，并在子进程结束后写退出码）。
- `serve` 启动时释放 callback 插件到 `<data>/ansible/callback_plugins/`，恢复上次运行中的作业，退出时停止 tail。

---

## 2. 端到端验证（模拟模式，127.0.0.1:8861）

命令：`go run ./cmd/pigsty-console serve --pigsty-home .. --data-dir ./.data --simulate --listen 127.0.0.1:8861`
（登录用本地 `.data` 中的 `e2e` 账号；验证结束后服务已停止，8860 上的实例未被触碰）

| 步骤 | 结果摘要 |
|---|---|
| `GET /meta` | `simulate=true`，`pigstyVersion=v4.5.0`，`inventoryEngine=builtin` |
| `GET /actions` | 20 条动作，`params/enum/help/of` 字段齐全 |
| `POST /actions/pgsql-user/plan` | `low`，`./pgsql-user.yml -l pg-meta --check -e username=dbuser_meta`，`gate=null` |
| `POST /actions/pgsql-user/plan`（非法集群名） | **422** + `issues[0].path=cluster`（`pg-meta; rm -rf /` 被白名单拒绝） |
| `POST /actions/pitr-inplace/plan` | `critical`，`gate.confirmText=pg-meta`，2 个 checkbox，`backupMaxAgeHours` 生效 |
| `POST /actions/pg-restart/plan` / `pgsql-rm-replica/plan` | 高风险 gate（confirmText=实例 IP）；移除主库被 `not_primary` 阻断 |
| `POST /jobs` → `GET /jobs/{id}/events` | 201；SSE `event: job` 69 条 + `event: end` 1 条，作业耗时 **17s**，`stats` 按主机汇总，退出码 0 |
| `GET /jobs/{id}/log` | `text/plain`，25 行 ansible 风格输出（`PLAY [...]`、`changed/skipping`、`PLAY RECAP`） |
| `GET /jobs` / `GET /jobs/{id}` | 倒序列表 + `total`；详情含 `stats/currentStep/exitCode/finishedAt` |
| `POST /jobs/{id}/cancel` | 204 → 状态 `canceled`，`finishedAt` 落库 |
| `GET /applied` | `pg-meta` 指向最近成功作业的 commit，`baseline=false`；未执行过的集群为基线 commit |
| `GET /status/nodes` | `source.url=simulate://victoria/nodes`，节点 CPU/内存/磁盘指标齐全 |
| `GET /status/clusters` + `/pgconfig` | Patroni 成员/leader/healthy；DCS 参数（`shared_buffers=2048MB` 等） |
| `GET /status/drift` | 1 条 `role_mismatch`（附回写建议）+ 1 条 `unknown_member` |
| `GET /status/alerts` / `/status/nodes/{ip}/memory` | `PgExporterDown`；19 个服务的内存实际值 |
| `GET /backups/pg-meta` | 7 份备份（full/incr 混合）、repo=local/posix、archive 区间、可恢复窗口、`restorePoints`/`events` 数组非 null |
| `POST /backups/pg-meta/pitr/plan`（inplace，窗口内） | `backupSet`、`inWindow=true`、`lossWindow`、`estimatedSeconds=78`、gate=2 checkbox |
| `POST /backups/pg-meta/pitr/plan`（clone） | `repo_reachable=false`（local 仓库不能跨节点）、`gate=null` |
| 高风险作业（`pg-restart`） | 无 confirm → **422**；带 confirm → 201；同时提交第二个变更作业 → **409 + runningJobId**；结束后 `restorePoint=console_job_20261016-0009`（已进 `restore_points`） |
| `GET/PUT /settings`、`GET/PUT/DELETE /node-specs` | 默认值/推导地址正确；非法 URL 与越界数值 422；node-spec 200/204/404 正确 |

常驻内存（`proc_pid_rusage`）：

| 时点 | resident_size | phys_footprint |
|---|---|---|
| 启动后空闲 | 19.6 MiB | 8.8 MiB |
| 完整 e2e（多个作业 + SSE）结束后 | 44.1–44.3 MiB | 14.9–31.0 MiB |

`GODEBUG=madvdontneed=1` 在 macOS 上没有区别（Darwin 用 `MADV_FREE_REUSABLE`，归还页仍计入 resident_size），
生产是 Linux + systemd（单元文件已带该变量）——见 §5.1。

---

## 3. 验证边界（本轮**未能**验证的部分）

本轮所有对外部系统的调用都在**模拟模式**下验证；以下行为必须在真实沙箱上用真实 Pigsty 集群复验：

1. **ansible 路径**：本机没有 `ansible`/`ansible-playbook`，`./pgsql.yml`、`ansible <ip> -i pigsty.yml …`、
   systemd-run 运行器、`exec-step` 的真实子进程退出码链路全部只跑了单测与静态检查。
2. **callback 插件**：`pigsty_events.py` 通过了 `python3 -m py_compile` 语法检查，但没有被真实 ansible 加载过
   （需核实 3 个环境变量注入、ad-hoc 模式下 `v2_playbook_on_play_start` 是否被调用、`no_log` 任务确实不出 result）。
3. **pgBackRest 输出**：解析器/窗口计算基于文档格式样例 JSON 的单测；真实 `pgbackrest info --output=json`
   的字段（尤其 `repo[].path`、`archive[]` 是否存在、`status.code` 语义）需实测核对。
4. **归档指标**：`pg_archiver_finish_time/failed_count/failed_time` 是从本仓库 `pg_exporter.yml` 推出来的指标名，
   未在真实 VictoriaMetrics 上查询过；`lastArchivedAt` 取不到时窗口会退化为「最新备份 stop」。
5. **Patroni REST**：`/cluster`、`/config` 的字段与认证/TLS 行为未连过真实集群；`patronictl -c /pg/bin/patroni.yml`
   这一路径需核实（本仓库 v4.5.0 的 `pgsql` role 实际把配置放在 `/etc/patroni/patroni.yml`，
   `/pg/bin/patroni.yml` 只在 `files/postgres/pg-alias` 里以 `/infra/conf/patronictl.yml` 出现；契约与 DESIGN §8.2 均写
   `/pg/bin/patroni.yml`，故按契约实现，**如真实环境不同请改 catalog.yaml 一处即可**）。
6. **`pitr-promote`**：契约命令 `./pgsql-pitr.yml -l <cls> -t up` 已按字面实现；真实 `pg_pitr` role 的 tag 名称与
   `action: pause` 后的提升流程（`pg_ctl promote` / `pg_wal_replay_resume()`）需在沙箱确认。
7. **内存/磁盘的 ad-hoc 与真实值**：`systemctl show … -p MemoryCurrent` 的 unit 名单（含 `redis-6379.service` 等）
   与实际部署的 unit 命名需核对；PITR 磁盘检查用「最新备份大小」近似数据目录大小。
8. **systemd 运行器**：`systemctl --user is-active`、`systemctl --user kill --signal=SIGINT`、
   `loginctl enable-linger` 的实际行为（以及 console 重启后 `systemctl show` 的衔接）需要一台真正的 systemd 机器。

---

## 4. 与契约的偏差

字段名、状态码、扩展字段（`issues` / `runningJobId` / `currentText`+`currentHash`）逐条对齐，未做删改。以下是契约未覆盖或需要补充说明的地方，**没有功能性偏差**：

1. **未知参数**一律 422（契约只要求“参数校验失败 → 422”，未说明未知参数）；模拟模式下额外允许 `fail: true`（契约 §7 要求），
   该参数只在 `--simulate` 时存在，真实模式提交 `fail` 会被 422 拒绝。
2. **`check: true` 对不支持 `--check` 的动作**（如 `pgsql-ext`、`pgsql-create`）返回 422（`issues[].path="check"`），
   而不是静默忽略。
3. **`pgsql-db` 的 `state: recreate`** 也升级为 high 风险并走闸门（契约只写了 `absent`）；`recreate` 会删库重建，按同一逻辑处理更安全。
4. **`ActionDef.gate`** 对 `pgsql-rm-replica`/`pg-restart`/`pg-switchover`/`pitr-inplace`/`pitr-promote` 均为 `true`
   （与 `ActionPlan.gate = gate || risk ≥ high` 保持一致，前端可只看一个字段）。
5. **`/status/clusters` 的并发**：契约要求“单个失败不影响其他”，实现为每个集群一个 goroutine（15s 缓存），
   失败只体现在该集群的 `source.error`；`/status/clusters` 本身不返回顶层 `source`（契约未定义）。
6. **`GET /applied` 的集群集合**：PGSQL + REDIS 分组的 identity（`pg_cluster`/`redis_cluster`），
   node 级动作（`cluster: null`）不产生 applied 记录（契约未明确，按“cluster → commit”语义处理）。
7. **作业 id**：`YYYYMMDD-NNNN`，序号按当天已存在的作业数递增（跨重启连续）。
8. **`Job.title`**：动作中文名 + 关键参数（用户/库/恢复点/主机），如「创建/更新业务用户 dbuser_meta @ pg-meta」。
9. **`pg_role`/`pg_seq` 的读取范围**：一个主机可能同时属于 infra/etcd/pgsql 等多个分组，实例级事实必须从
   「该集群自身分组」的 host vars 读取（新增 `inventory.Model.HostVarsIn`）。这是本轮修掉的一个真实缺陷：
   按“第一个包含该主机分组的 host vars”读取时，`10.10.10.10` 会先命中 `app` 组（空 vars），
   导致 `pg_role`/`pg_seq` 丢失、`pg-restart` 渲染出 IP 而不是 `pg-meta-1`、主库移除检查被绕过。
10. **`--simulate` 为显式开关**：`/meta.simulate` 反映其状态；环境变量 `PIGSTY_CONSOLE_SIMULATE=1/true` 等价。
11. **多个内嵌包**：新增 `internal/plan`（共享契约类型）与 `internal/services`（装配层），
    目的是打破 `actions ↔ backup/status` 的循环依赖；对外契约无影响。

---

## 4.1 单元测试清单（本轮新增，按任务书“质量要求”对应）

| 要求 | 测试 |
|---|---|
| 动作参数校验与命令渲染（拒绝缺 `-l`、拒绝非法字符、拒绝移除主库） | `TestCatalogLoadsMVP`、`TestPlanRendersWhitelistedCommands`、`TestPlanRejectsBadParameters`、`TestAssertArgvRejectsUnlimitedCommands`、`TestEveryRenderedCommandIsGuarded`、`TestPlanRefusesPrimaryRemoval`、`TestPlanUpgradesRiskForDestructiveUser`、`TestPlanClusterAbsentForCreate`、`TestPlanNodeAddWarnsUnknownHost`、`TestPlanLocalRepoBlocksClone`、`TestPlanPitrInplaceGateAndPayload` |
| pgbackrest info 解析与窗口计算 | `TestParseInfo`、`TestParseInfoRejectsGarbage`、`TestComputeWindowAndSelection`、`TestComputeWindowWithoutBackups`、`TestSimulatedWindowGrowsWithTime` |
| PITR plan | `TestAssessPitrInplace`、`TestAssessPitrOutsideWindow`、`TestAssessPitrBeforeFirstBackup`、`TestAssessPitrLocalRepoClone`、`TestAssessPitrDiskSpace`、`TestPgPitrRendering`、`TestParseTargetRejectsBadValues`、`TestParsePitrTimeFormats` |
| 作业并发锁 | `TestSingleMutatingJobLock`、`TestReadonlyJobsRunInParallel`、`TestSubmitRejectsBlockedPreconditions`、`TestSubmitRequiresGateConfirmation` |
| 脱敏 | `TestSanitizerMasksSecrets` |
| 模拟作业的事件序列完整性 | `TestSimulatedJobEventSequence`、`TestSimulatedJobFailure`、`TestCancelSimulatedJob`、`TestHighRiskJobCreatesRestorePoint`、`TestRestorePointActionIsRecorded`、`TestAppliedVersionIsRecorded`、`TestReattachFinishedJobAfterRestart`、`TestEventBusReleasesSubscribers` |
| 状态模拟数据与漂移 | `TestSimulatedNodesAreStableAndComplete`、`TestSimulatedClusterHasOneRoleMismatch`、`TestSimulatedAlertsAndConfig`、`TestDerivedURLsAndOverrides`、`TestSimulatedMemoryIsStable`、`TestParseSystemctlMemory`、`TestParseVMLabels`、`TestParseLagValues` |
| HTTP 契约（含 SSE 到 end、409/422 扩展字段、数组非 null） | `TestActionsCatalogAndPlan`、`TestJobLifecycle`、`TestSingleMutatingJobReturnsConflict`、`TestAppliedBaseline`、`TestStatusEndpoints`、`TestSettingsAndNodeSpecs`、`TestBackupsAndPitrPlan`（M0–M1 的 `internal/api`、`internal/configstore` 等既有测试全部保持通过） |

---

## 5. 已知问题

1. **常驻内存（macOS 观测值）**：空闲 19.6 MiB resident / 8.8 MiB footprint；完整 e2e 后 44 MiB resident / 15–31 MiB footprint。
   macOS 的 `MADV_FREE_REUSABLE` 使归还页仍计入 `resident_size`，`GODEBUG=madvdontneed=1` 在 Darwin 上无效。
   生产单元文件已带该变量，**建议在真实 1c1g Linux 上复测空闲 RSS**（M0–M1 报告已提同一问题）。
   本轮已在「作业结束」与「SSE 断开」两个回收点主动 `debug.FreeOSMemory()`，footprint 明显回落。
2. **模拟模式的落库残留**：`.data/` 里留有本轮 e2e 产生的作业记录（`20261016-0001…0012`）、`applied` 行、
   恢复点与 jobs 目录；`.data/` 已在 `console/.gitignore` 中。若需要干净状态，删除 `.data` 后重新 `init`/`user add` 即可。
3. **模拟模式默认时长**：单作业 13–20s（真实节奏，按契约 10–30s）；前端联调如嫌慢，可用 `jobs.Options.SimulateSpeed`
   调快（api 测试用 400 倍，本机 e2e 用默认 1 倍）。
4. **PID 复用风险**：进程组模式在 console 重启后用 PID 文件判定存活，理论上存在 PID 复用导致的误判；
   systemd 模式用 `is-active` 更可靠。两种模式都会以「退出码文件」为最终判据，误判只影响“是否还在运行”的短窗口。
5. **`.data/console.db` 与本机 8860 实例**：本轮新建了本地 `e2e` 账号用于 curl 验证（密码 `E2e.Password.123`，
   仅存在于本地 `.data`，未写入任何版本控制文件）；8860 端口上的用户实例从头到尾没有被触碰。
6. **configstore 既有测试偶发**：`TestWatchCommitsExternalChangeOnly` 在整包并行负载下偶发“author=admin”，
   原因是 git 提交时间戳同秒时的排序不稳定（与本轮改动无关，单独运行与多次 `-count=1` 均通过）。
