// 与 console/API.md 保持一致。后端可用后可用 `pnpm gen:api` 生成 openapi.d.ts 做核对。

export type IssueLevel = 'error' | 'warning'
export type IssueSource = 'yaml' | 'schema' | 'semantic' | 'validate' | 'inventory'

export interface Issue {
  level: IssueLevel
  source: IssueSource
  message: string
  path?: string
  line?: number
  column?: number
}

export interface CommitInfo {
  hash: string
  shortHash: string
  author: string
  message: string
  time: string
}

export interface Meta {
  version: string
  pigstyVersion: string
  pigstyHome: string
  inventoryEngine: 'ansible' | 'builtin'
  validateAvailable: boolean
  simulate?: boolean
}

export interface ConfigDoc {
  text: string
  hash: string
  commit: CommitInfo | null
}

export interface SaveResult {
  hash: string
  commit: CommitInfo
}

export interface ValidateResult {
  ok: boolean
  issues: Issue[]
}

export interface HistoryPage {
  items: CommitInfo[]
  total: number
}

export interface DiffResult {
  from: string
  to: string
  fromText: string
  toText: string
  unified: string
}

export type InventoryModule = 'PGSQL' | 'INFRA' | 'ETCD' | 'MINIO' | 'REDIS' | 'APP'

export interface InventoryHosts {
  hosts: { ip: string; groups: string[] }[]
  groups: { name: string; hosts: string[]; module: InventoryModule | null }[]
}

export interface EffectiveVar {
  key: string
  value: unknown
  source: 'default' | 'global' | 'group' | 'host'
  sourceGroup?: string
  sensitive: boolean
}

export interface EffectiveVars {
  host: string
  engine: 'ansible' | 'builtin'
  vars: EffectiveVar[]
}

export type ParamType = 'string' | 'integer' | 'number' | 'boolean' | 'array' | 'object' | 'any'

export interface ParamDef {
  module: string
  section?: string
  role: string
  level: string
  type: ParamType
  default?: unknown
  enum?: unknown[]
  identity?: boolean
  sensitive?: boolean
  impact?: string
  desc?: string
  label?: string
  doc?: string
  items?: unknown
  $ref?: string
}

export interface ParamSchema {
  version: string
  modules: string[]
  params: Record<string, ParamDef>
  defs: Record<string, unknown>
}

// ---------- M2–M5（console/API-M2-M5.md） ----------

export type Risk = 'readonly' | 'low' | 'medium' | 'high' | 'critical'

export interface SourceInfo {
  ok: boolean
  url: string
  error: string | null
  at: string
}

export interface Precondition {
  id: string
  ok: boolean
  level: IssueLevel
  message: string
}

export type ActionParamType =
  | 'pg_cluster'
  | 'redis_cluster'
  | 'pg_member'
  | 'ip_list'
  | 'pg_user'
  | 'pg_db'
  | 'enum'
  | 'string'
  | 'bool'
  | 'int'
  | 'datetime'
  | 'object'

export interface ActionParam {
  name: string
  label: string
  type: ActionParamType
  of: string | null
  required: boolean
  enum: string[]
  default: unknown
  help: string | null
}

export interface ActionDef {
  id: string
  title: string
  description: string
  category: 'pgsql' | 'node' | 'redis' | 'backup' | 'patroni'
  risk: Risk
  check: boolean
  gate: boolean
  params: ActionParam[]
}

export interface Gate {
  confirmText: string
  checkboxes: string[]
  lastBackupAt: string | null
  backupMaxAgeHours: number
}

export interface ActionPlan {
  action: string
  risk: Risk
  commands: string[]
  targets: string[]
  preconditions: Precondition[]
  gate: Gate | null
  configHash: string
}

export type JobStatus = 'queued' | 'running' | 'success' | 'failed' | 'canceled'

export interface HostStats {
  ok: number
  changed: number
  failed: number
  unreachable: number
  skipped: number
}

export interface RestorePoint {
  name: string
  lsn: string | null
  time: string
  jobId: string | null
  cluster: string
}

export interface Job {
  id: string
  action: string
  title: string
  params: Record<string, unknown>
  check: boolean
  risk: Risk
  status: JobStatus
  commands: string[]
  cluster: string | null
  targets: string[]
  configHash: string
  configCommit: string | null
  user: string
  confirmText: string | null
  createdAt: string
  startedAt: string | null
  finishedAt: string | null
  exitCode: number | null
  currentStep: number
  stats: Record<string, HostStats>
  restorePoint: RestorePoint | null
  error: string | null
}

export interface JobPage {
  items: Job[]
  total: number
}

export type TaskStatus = 'ok' | 'changed' | 'failed' | 'skipped' | 'unreachable'

export interface JobEvent {
  seq: number
  ts: string
  type:
    | 'step_start'
    | 'step_end'
    | 'playbook_start'
    | 'play_start'
    | 'task_start'
    | 'runner_result'
    | 'stats'
    | 'log'
    | 'status'
  step?: number
  exitCode?: number
  playbook?: string
  play?: string
  task?: string
  taskId?: string
  host?: string
  status?: TaskStatus
  durationMs?: number
  msg?: string
  line?: string
  hosts?: Record<string, HostStats>
  job?: Job
}

export interface SubmitJob {
  action: string
  params: Record<string, unknown>
  check: boolean
  confirm?: { text: string; checkboxes: string[] }
}

export interface Applied {
  cluster: string
  commit: string
  jobId: string | null
  at: string
  baseline: boolean
}

export interface NodeStatus {
  ip: string
  up: boolean | null
  cpuCores: number | null
  cpuUsage: number | null
  load1: number | null
  memTotalBytes: number | null
  memAvailableBytes: number | null
  diskTotalBytes: number | null
  diskFreeBytes: number | null
  managed: boolean
}

export interface PatroniMember {
  name: string
  host: string
  port: number
  role: string
  state: string
  timeline: number | null
  lagBytes: number | null
  pendingRestart: boolean
}

export interface ClusterStatus {
  cluster: string
  source: SourceInfo
  paused: boolean | null
  leader: string | null
  members: PatroniMember[]
  healthy: boolean | null
}

export interface Drift {
  kind: 'role_mismatch' | 'missing_member' | 'unknown_member'
  cluster: string
  host: string
  message: string
  fix: { primary: string; replicas: string[] } | null
}

export interface Alert {
  name: string
  severity: string
  summary: string
  startsAt: string
  labels: Record<string, string>
}

export interface Settings {
  victoriaUrl: string
  alertmanagerUrl: string
  grafanaUrl: string
  backupMaxAgeHours: number
  ansibleForks: number
  memoryBaseline: Record<string, number>
  derived: { victoriaUrl: string; alertmanagerUrl: string; grafanaUrl: string }
}

export interface NodeSpec {
  ip: string
  cpu: number
  memMb: number
  diskGb: number
  updatedAt: string
}

export interface Backup {
  label: string
  type: 'full' | 'diff' | 'incr'
  prior: string | null
  start: string
  stop: string
  size: number
  deltaSize: number
  repoSize: number
  repoDeltaSize: number
  lsnStart: string | null
  lsnStop: string | null
  walStart: string | null
  walStop: string | null
  error: boolean
}

export interface TimelineEvent {
  time: string
  kind: 'pitr' | 'failover' | 'job'
  label: string
  jobId: string | null
}

export interface BackupInfo {
  cluster: string
  source: SourceInfo
  stanza: string
  repo: { method: string; type: string; path: string | null; retention: string | null; cipher: string | null }
  status: { code: number; message: string } | null
  backups: Backup[]
  archive: {
    min: string | null
    max: string | null
    lastArchivedAt: string | null
    failedCount: number | null
    lastFailedAt: string | null
  }
  window: { start: string; end: string } | null
  restorePoints: RestorePoint[]
  events: TimelineEvent[]
  fetchedAt: string
}

export type PitrTargetType = 'latest' | 'time' | 'name' | 'lsn' | 'xid' | 'immediate'

// type 而不是 interface：需要能赋给后端声明的 map 类型
export type PitrTarget = {
  type: PitrTargetType
  time?: string
  name?: string
  lsn?: string
  xid?: string
  set?: string
  exclusive?: boolean
}

export type PitrAction = 'pause' | 'promote' | 'shutdown'

export interface PitrPlanRequest {
  mode: 'clone' | 'inplace'
  target: PitrTarget
  action: PitrAction
  backup: boolean
  newCluster?: string
}

export interface PitrPlan {
  backupSet: string | null
  inWindow: boolean
  lossWindow: { from: string; to: string } | null
  estimatedSeconds: number | null
  pgPitr: Record<string, unknown>
  commands: string[]
  preconditions: Precondition[]
  gate: Gate | null
}
