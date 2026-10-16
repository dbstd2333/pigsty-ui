# Pigsty Console API 契约（M2–M5）

在 `API.md`（M0–M1）基础上新增：动作目录、作业、已应用版本、状态采集、节点规格、设置、备份与 PITR。
约定同 `API.md`：基础路径 `/api/v1`、camelCase、problem+json 错误、写请求同源校验、全部需登录。
**数组字段一律返回 `[]` 而不是 `null`**；可空的标量/对象字段用 `null`，不要省略。时间一律 RFC 3339 字符串。

## 公共类型

```ts
type Risk = "readonly" | "low" | "medium" | "high" | "critical";

// 外部数据源（VictoriaMetrics / Patroni / Alertmanager / ansible ad-hoc）的取数情况
type SourceInfo = {
  ok: boolean;
  url: string;          // 实际访问的地址；ad-hoc 时为 "ansible:<host>"
  error: string | null; // 失败原因（连不上、超时、未配置等）
  at: string;           // 取数时间
};

type Precondition = {
  id: string;           // 如 "cluster_exists"、"backup_fresh"、"not_primary"
  ok: boolean;
  level: "error" | "warning";   // error 且 ok=false 时禁止提交
  message: string;              // 中文说明
};
```

## 1. 动作目录

动作目录是后端内置的白名单（`internal/actions/catalog.yaml` 嵌入），前端只能提交 `{action, params, check, confirm}`，不能提交命令。

`GET /actions` → `{ items: ActionDef[] }`

```ts
type ActionDef = {
  id: string;
  title: string;            // 中文
  description: string;      // 中文，一两句话
  category: "pgsql" | "node" | "redis" | "backup" | "patroni";
  risk: Risk;
  check: boolean;           // 是否支持 --check 预演
  gate: boolean;            // 是否走高风险闸门（输入目标名确认）
  params: ActionParam[];
};

type ActionParam = {
  name: string;
  label: string;            // 中文
  type:
    | "pg_cluster"          // inventory 中的 PGSQL 组名
    | "redis_cluster"       // inventory 中的 REDIS 组名
    | "pg_member"           // 某 PG 集群内的实例 IP（of 指向集群参数）
    | "ip_list"             // 若干 IP（of 存在时须属于该集群）
    | "pg_user"             // 某集群 pg_users 中的用户名（of 指向集群参数）
    | "pg_db"               // 某集群 pg_databases 中的库名（of 指向集群参数）
    | "enum" | "string" | "bool" | "int" | "datetime" | "object";
  of: string | null;
  required: boolean;
  enum: string[];           // type=enum 时的可选值，否则 []
  default: unknown | null;
  help: string | null;
};
```

`POST /actions/{id}/plan` ← `{ params: Record<string, unknown>; check: boolean }` →

```ts
type ActionPlan = {
  action: string;
  risk: Risk;                    // 可能因参数升级，如用户 state=absent 升为 high
  commands: string[];            // 渲染后的完整命令，每条一行，便于展示
  targets: string[];             // 涉及的主机 IP
  preconditions: Precondition[];
  gate: Gate | null;             // gate=true 或风险 ≥ high 时返回
  configHash: string;            // 计算计划时使用的 pigsty.yml hash
};

type Gate = {
  confirmText: string;           // 用户必须原样输入的文本（集群名或 IP）
  checkboxes: string[];          // 必须全部勾选的确认项（中文），如原地 PITR 的「业务已停写」
  lastBackupAt: string | null;   // 最近一次成功备份时间（取不到为 null）
  backupMaxAgeHours: number;     // 阈值（设置项，默认 26）
};
```

参数校验失败 → 422，problem 扩展字段 `{ issues: Issue[] }`（`path` 为参数名）。

### MVP 动作清单

| id | 分类 | 风险 | check | 命令（在 PIGSTY_HOME 下执行，`-l` 必须存在） |
|---|---|---|---|---|
| `pgsql-create` | pgsql | medium | 否 | `./pgsql.yml -l <cls>`；前置：集群在 Patroni 中无成员 |
| `pgsql-user` | pgsql | low | 是 | `./pgsql-user.yml -l <cls> -e username=<user>`；用户 `state: absent` 时 high |
| `pgsql-db` | pgsql | low | 是 | `./pgsql-db.yml -l <cls> -e dbname=<db>`；`state: absent` 时 high |
| `pgsql-hba` | pgsql | low | 是 | `./pgsql.yml -l <cls> -t pg_hba,pg_reload,pgbouncer_hba,pgbouncer_reload` |
| `pgsql-svc` | pgsql | low | 是 | `./pgsql.yml -l <cls> -t pg_service` |
| `pgsql-ext` | pgsql | low | 否 | `./pgsql.yml -l <cls> -t pg_ext` |
| `pgsql-crontab` | pgsql | low | 是 | `./pgsql.yml -l <cls> -t pg_crontab` |
| `pgsql-add-replica` | pgsql | medium | 否 | `./pgsql.yml -l <ips>` → `./pgsql.yml -l <cls> -t pg_service`；ips 必须在集群 hosts 中 |
| `pgsql-rm-replica` | pgsql | high | 否 | `./pgsql-rm.yml -l <ips>` → `./pgsql.yml -l <cls> -t pg_service`；目标不得是主库（inventory 的 pg_role 与 Patroni leader 双重判断） |
| `patroni-param` | patroni | medium | 否 | 主库上 dbsu：`patronictl -c /pg/bin/patroni.yml edit-config <cls> -s <k>=<v> --force`（params: cluster, settings: object） |
| `pg-restart` | patroni | high | 否 | `patronictl -c /pg/bin/patroni.yml restart <cls> <member> --force` |
| `pg-switchover` | patroni | high | 否 | `patronictl -c /pg/bin/patroni.yml switchover <cls> --leader <a> --candidate <b> --force` |
| `node-add` | node | medium | 否 | `./node.yml -l <ips>` |
| `redis-create` | redis | medium | 否 | `./redis.yml -l <cls>`；前置：节点已纳管（提示性 warning） |
| `backup-run` | backup | low | 否 | 主库 dbsu：`/pg/bin/pg-backup <full\|diff\|incr>` |
| `backup-check` | backup | readonly | 否 | 主库 dbsu：`pgbackrest --stanza=<stanza> check` |
| `restore-point` | backup | low | 否 | 主库：`psql -c "SELECT pg_create_restore_point('<name>')"`，结果写入 restore_points 表 |
| `pitr-clone` | backup | medium | 否 | `./pgsql-pitr.yml -l <新集群> -e '{"pg_pitr":{...,"cluster":"<源>"}}'` |
| `pitr-inplace` | backup | critical | 否 | `./pgsql-pitr.yml -l <cls> -e '{"pg_pitr":{...}}'`；闸门见 §6 |
| `pitr-promote` | backup | high | 否 | `./pgsql-pitr.yml -l <cls> -t up`（如沙箱验证不对，在报告中说明） |

「主库 dbsu」类动作统一用 ansible ad-hoc：
`ansible <primary_ip> -i pigsty.yml -b --become-user=<pg_dbsu|postgres> -m shell -a '<cmd>'`。
主库 IP 优先取 Patroni leader，取不到退回 inventory 中 `pg_role: primary` 的主机。

PITR 两个动作的参数：`cluster`（目标集群）、`source`（源集群，仅 clone）、`target: PitrTarget`、`action: "pause"|"promote"|"shutdown"`、`backup: bool`（仅 inplace）。
`PitrTarget = { type: "latest"|"time"|"name"|"lsn"|"xid"|"immediate"; time?: string; name?: string; lsn?: string; xid?: string; set?: string; exclusive?: boolean }`，
后端把它转换为 `pg_pitr`（`latest` → 不设 type；`time` 统一转成 `YYYY-MM-DD HH:MM:SS+00`）。

## 2. 作业

`POST /jobs` ← `{ action: string; params: Record<string, unknown>; check: boolean; confirm?: { text: string; checkboxes: string[] } }` → `Job`（201）

- 先重新计算 plan：error 级前置条件未通过 → 422（`issues`）；闸门确认不符 → 422。
- 已有变更作业在运行（只读动作不计）→ 409，problem 扩展字段 `{ runningJobId: string }`。
- 风险 ≥ high 的动作，执行前自动在目标 PG 集群主库创建恢复点 `console_job_<id>`（失败只记 warning 事件，不阻止）。

```ts
type JobStatus = "queued" | "running" | "success" | "failed" | "canceled";

type HostStats = { ok: number; changed: number; failed: number; unreachable: number; skipped: number };

type Job = {
  id: string;               // 短 id，如 "20261016-0003"
  action: string;
  title: string;            // 动作中文名 + 关键参数，如「创建用户 dbuser_app @ pg-test」
  params: Record<string, unknown>;
  check: boolean;
  risk: Risk;
  status: JobStatus;
  commands: string[];
  cluster: string | null;   // 主要涉及的集群
  targets: string[];
  configHash: string;
  configCommit: string | null;
  user: string;
  confirmText: string | null;
  createdAt: string;
  startedAt: string | null;
  finishedAt: string | null;
  exitCode: number | null;
  currentStep: number;      // 0-based，正在执行第几条命令
  stats: Record<string, HostStats>;  // 按主机汇总（来自 ansible stats 事件）
  restorePoint: RestorePoint | null; // 自动恢复点
  error: string | null;
};
```

`GET /jobs?limit=50&offset=0&cluster=&status=` → `{ items: Job[]; total: number }`（按创建时间倒序）

`GET /jobs/{id}` → `Job`

`GET /jobs/{id}/events?after=<seq>`（SSE，`text/event-stream`）：
- 先补发 `seq > after` 的历史事件，再实时推送；作业结束后发送 `event: end`（data 为最终 `Job`）并关闭。
- 每条 `event: job`，data 为 JSON：

```ts
type JobEvent = {
  seq: number;              // 作业内递增，从 1 开始
  ts: string;
  type:
    | "step_start" | "step_end"      // 第 step 条命令开始/结束（end 带 exitCode）
    | "playbook_start" | "play_start" | "task_start"
    | "runner_result"                // 某主机某 task 结果
    | "stats"                        // playbook 结束统计
    | "log"                          // 原始输出行（stdout，已脱敏）
    | "status";                      // 作业状态变化，data.job 为最新 Job
  step?: number;
  exitCode?: number;
  playbook?: string;
  play?: string;
  task?: string;
  taskId?: string;
  host?: string;
  status?: "ok" | "changed" | "failed" | "skipped" | "unreachable";
  durationMs?: number;
  msg?: string;
  line?: string;
  hosts?: Record<string, HostStats>;
  job?: Job;
};
```

`GET /jobs/{id}/log` → `text/plain`，完整原始输出（已脱敏）。

`POST /jobs/{id}/cancel` → 204（SIGINT，10 秒后 SIGTERM）。

## 3. 已应用版本

`GET /applied` → `{ items: Applied[] }`

```ts
type Applied = {
  cluster: string;
  commit: string;           // 该集群最近一次成功（非 check）作业时的配置 commit；从未执行过时为 init 首个 commit
  jobId: string | null;
  at: string;
  baseline: boolean;        // true = 尚未通过 console 执行过，使用基线 commit
};
```

前端用 `GET /config/history/{commit}` 取出该版本文本，和当前配置做语义 diff，推导建议动作。

## 4. 状态采集

所有状态接口都允许外部数据源不可用：此时 `source.ok=false`，其余字段尽量给出 inventory 能推导的部分，HTTP 仍返回 200。
后端对每个源做 15 秒内存缓存，**只在有请求时才取数**，不做常驻轮询。

`GET /status/nodes` → `{ source: SourceInfo; items: NodeStatus[] }`

```ts
type NodeStatus = {
  ip: string;
  up: boolean | null;              // null = 无指标
  cpuCores: number | null;
  cpuUsage: number | null;         // 0–1，近 1 分钟
  load1: number | null;
  memTotalBytes: number | null;
  memAvailableBytes: number | null;
  diskTotalBytes: number | null;   // 数据盘（/data 或 pg_fs_main 所在挂载点，取不到用 /）
  diskFreeBytes: number | null;
  managed: boolean;                // 是否已纳管（有 node_exporter 指标）
};
```
来源：VictoriaMetrics PromQL（`up{job="node"}`、`node_cpu_seconds_total`、`node_load1`、`node_memory_MemTotal_bytes`、`node_memory_MemAvailable_bytes`、`node_filesystem_*`），以 `ip` / `instance` 标签匹配。实际 job 名与标签以 Pigsty 的 VictoriaMetrics 抓取配置为准（`roles/infra` 里核实）。

`GET /status/clusters` → `{ items: ClusterStatus[] }`（只含 PGSQL 集群；每个集群并发取 Patroni，单个失败不影响其他）

`GET /status/clusters/{cls}` → `ClusterStatus`

```ts
type ClusterStatus = {
  cluster: string;
  source: SourceInfo;              // Patroni REST，依次尝试集群成员 :8008/cluster
  paused: boolean | null;
  leader: string | null;           // leader 的 IP
  members: PatroniMember[];
  healthy: boolean | null;         // 有 leader 且所有成员 running/streaming
};

type PatroniMember = {
  name: string;                    // 如 pg-test-1
  host: string;                    // IP
  port: number;
  role: "leader" | "replica" | "sync_standby" | "standby_leader" | string;
  state: string;                   // running / streaming / stopped / ...
  timeline: number | null;
  lagBytes: number | null;
  pendingRestart: boolean;
};
```
Patroni 开启 TLS 时使用 `<pigsty-home>/files/pki/ca.crt` 校验（只读 ca.crt，**绝不读取 ca.key**）。Patroni REST 需要认证时使用 inventory 中的 `patroni_username`/`patroni_password`。

`GET /status/clusters/{cls}/pgconfig` → `{ source: SourceInfo; parameters: Record<string, string> }`
来源：Patroni `GET :8008/config` 中的 `postgresql.parameters`（DCS 中实际生效值），数值统一转成字符串。

`GET /status/drift` → `{ items: Drift[] }`

```ts
type Drift = {
  kind: "role_mismatch" | "missing_member" | "unknown_member";
  cluster: string;
  host: string;
  message: string;                 // 中文
  fix: { primary: string; replicas: string[] } | null;  // role_mismatch 时给出应回写的 pg_role
};
```

`GET /status/alerts` → `{ source: SourceInfo; items: Alert[] }`

```ts
type Alert = { name: string; severity: string; summary: string; startsAt: string; labels: Record<string, string> };
```

`GET /status/nodes/{ip}/memory` → `{ source: SourceInfo; services: { name: string; bytes: number | null }[] }`
来源：ansible ad-hoc 只读 `systemctl show <svc> -p MemoryCurrent`（patroni、postgresql、pgbouncer、pg_exporter、etcd、minio、redis*、vector、node_exporter、haproxy、grafana-server、victoria-metrics、victoria-logs、nginx），结果缓存 10 分钟；`?refresh=1` 强制刷新。

## 5. 设置与节点规格

`GET /settings` → `Settings`；`PUT /settings` ← `Partial<Settings>` → `Settings`

```ts
type Settings = {
  victoriaUrl: string;          // 空串 = 自动推导（infra 组第一台 + 默认端口）
  alertmanagerUrl: string;
  grafanaUrl: string;           // 用于深链
  backupMaxAgeHours: number;    // 默认 26
  ansibleForks: number;         // 默认 5
  memoryBaseline: Record<string, number>;   // 前端经验值表的本地覆盖（组件 id → MB）
  derived: { victoriaUrl: string; alertmanagerUrl: string; grafanaUrl: string };  // 只读：自动推导出的地址
};
```

`GET /node-specs` → `{ items: NodeSpec[] }`；`PUT /node-specs/{ip}` ← `{ cpu, memMb, diskGb }` → `NodeSpec`；`DELETE /node-specs/{ip}` → 204

```ts
type NodeSpec = { ip: string; cpu: number; memMb: number; diskGb: number; updatedAt: string };
```
用于尚未纳管（无指标）的节点做容量规划，不写入 pigsty.yml。

## 6. 备份与 PITR

`GET /backups/{cls}?refresh=0|1` → `BackupInfo`（缓存 10 分钟；备份/恢复类作业结束后自动失效）

```ts
type BackupInfo = {
  cluster: string;
  source: SourceInfo;              // ansible ad-hoc：主库 dbsu 执行 pgbackrest info --output=json
  stanza: string;
  repo: {
    method: string;                // pgbackrest_method
    type: string;                  // posix / s3 / ...
    path: string | null;
    retention: string | null;      // 如 "full: 2" / "time: 14 days"
    cipher: string | null;         // 只显示算法，绝不返回口令
  };
  status: { code: number; message: string } | null;   // pgbackrest stanza status
  backups: Backup[];               // 按开始时间升序
  archive: { min: string | null; max: string | null; lastArchivedAt: string | null; failedCount: number | null; lastFailedAt: string | null };
  window: { start: string; end: string } | null;      // 可恢复窗口：最早备份 stop → 最新归档时间
  restorePoints: RestorePoint[];
  events: TimelineEvent[];         // console 执行的 PITR / 高风险作业、时间线切换
  fetchedAt: string;
};

type Backup = {
  label: string;
  type: "full" | "diff" | "incr";
  prior: string | null;            // 依赖的父备份 label
  start: string;
  stop: string;
  size: number;                    // 数据库大小 bytes
  deltaSize: number;
  repoSize: number;                // 压缩后
  repoDeltaSize: number;
  lsnStart: string | null;
  lsnStop: string | null;
  walStart: string | null;
  walStop: string | null;
  error: boolean;
};

type RestorePoint = { name: string; lsn: string | null; time: string; jobId: string | null; cluster: string };

type TimelineEvent = { time: string; kind: "pitr" | "failover" | "job"; label: string; jobId: string | null };
```

`POST /backups/{cls}/pitr/plan` ← `{ mode: "clone" | "inplace"; target: PitrTarget; action: "pause"|"promote"|"shutdown"; backup: boolean; newCluster?: string }` →

```ts
type PitrPlan = {
  backupSet: string | null;        // 将使用的备份集（目标之前最近的一个）
  inWindow: boolean;
  lossWindow: { from: string; to: string } | null;  // 原地恢复将丢失的数据区间（目标 → 现在）
  estimatedSeconds: number | null; // 备份大小 / 经验吞吐（标注估算）
  pgPitr: Record<string, unknown>; // 渲染出的 pg_pitr
  commands: string[];
  preconditions: Precondition[];   // 例：local 仓库不能恢复到其他节点、磁盘空间是否足够 backup:true
  gate: Gate | null;
};
```

原地 PITR（`pitr-inplace`）闸门：`confirmText` = 集群名；`checkboxes` 至少包含「我已确认业务已停写或已通知相关方」；`backup: true` 时检查数据盘剩余空间 ≥ 数据目录大小（取不到指标时为 warning）。执行期间该集群的其他变更作业返回 409。

## 7. 开发模拟模式

`serve --simulate`（或环境变量 `PIGSTY_CONSOLE_SIMULATE=1`）：
- 作业不调用真实 ansible，而是按动作生成一段逼真的事件流（每个目标主机若干 task，约 10–30 秒结束，`node-add`/`pitr-*` 更长），`log` 行模仿 ansible 输出；参数含 `fail: true` 时让最后一个 task 失败，便于测试失败态。
- 状态接口返回基于 inventory 生成的稳定假数据（节点规格、Patroni 成员、几次备份、恢复窗口、一两条告警/漂移），`source.url` 标为 `simulate://`。
- `/meta` 增加字段 `simulate: boolean`，前端据此显示「模拟模式」标记。
