// 内存预估（DESIGN §12）。PG 参数公式移植自 roles/pgsql/templates/{oltp,olap,crit,tiny}.yml（Pigsty v4.5.0）。
import type { ParamSchema } from '@/api/types'
import type { ConfigModel, Vars } from '@/features/config/model'
import { BASELINE, type BaselineId } from './baseline'

export type PgConf = 'oltp' | 'olap' | 'crit' | 'tiny'

interface TemplateSpec {
  maxConn: (dest: string) => number
  maintenanceRatio: number
  workMemMin: number
  workMemMax: number
  autovacuumWorkers: number
}

export const TEMPLATES: Record<PgConf, TemplateSpec> = {
  oltp: { maxConn: (d) => (d === 'pgbouncer' ? 500 : 1000), maintenanceRatio: 0.25, workMemMin: 64, workMemMax: 1024, autovacuumWorkers: 3 },
  olap: { maxConn: () => 500, maintenanceRatio: 0.5, workMemMin: 64, workMemMax: 8192, autovacuumWorkers: 3 },
  crit: { maxConn: (d) => (d === 'pgbouncer' ? 500 : 1000), maintenanceRatio: 0.25, workMemMin: 64, workMemMax: 1024, autovacuumWorkers: 3 },
  tiny: { maxConn: () => 250, maintenanceRatio: 0.25, workMemMin: 16, workMemMax: 256, autovacuumWorkers: 2 },
}

export const WAL_BUFFERS_MB = 16
export const HASH_MEM_MULTIPLIER = 8

export interface PgParams {
  conf: PgConf
  maxConnections: number
  sharedBuffers: number
  maintenanceWorkMem: number
  workMem: number
  effectiveCacheSize: number
  autovacuumWorkers: number
}

export function pgConfName(v: unknown): PgConf {
  const s = String(v ?? 'oltp.yml').replace(/\.ya?ml$/, '')
  return (s in TEMPLATES ? s : 'oltp') as PgConf
}

/** 与模板 {% set %} 段逐项对应 */
export function pgParams(memMb: number, conf: PgConf, maxConn: unknown, ratio: number, serviceDest: string): PgParams {
  const t = TEMPLATES[conf]
  const mc = maxConn !== 'auto' && Number(maxConn) >= 20 ? Math.trunc(Number(maxConn)) : t.maxConn(serviceDest)
  const sb = Math.ceil(memMb * ratio)
  return {
    conf,
    maxConnections: mc,
    sharedBuffers: sb,
    maintenanceWorkMem: Math.ceil(sb * t.maintenanceRatio),
    workMem: Math.min(Math.max(Math.floor(sb / mc), t.workMemMin), t.workMemMax),
    effectiveCacheSize: memMb - sb,
    autovacuumWorkers: t.autovacuumWorkers,
  }
}

/** "1GB" / "64MB" / "512kb" / 数字（字节）→ MB */
export function parseSizeMb(v: unknown): number {
  if (typeof v === 'number') return v / 1024 / 1024
  const m = String(v ?? '').trim().match(/^(\d+(?:\.\d+)?)\s*([kmgt]?)i?b?$/i)
  if (!m) return 0
  const n = Number(m[1])
  const unit = m[2].toLowerCase()
  return unit === 'k' ? n / 1024 : unit === 'm' ? n : unit === 'g' ? n * 1024 : unit === 't' ? n * 1024 * 1024 : n / 1024 / 1024
}

// ---------- 节点汇总 ----------

export interface NodeSpecInput {
  memMb: number
  cpu: number
  source: 'metrics' | 'spec' | 'assumed'
}

export interface Assumptions {
  activeConn: number | null // null = min(max_connections, 4 × CPU)
  perConnMb: number
  redisFactor: number
  reservePct: number
  reserveMinMb: number
}

export const DEFAULT_ASSUMPTIONS: Assumptions = {
  activeConn: null,
  perConnMb: 10,
  redisFactor: 1.3,
  reservePct: 0.05,
  reserveMinMb: 512,
}

export type Layer = 'pg' | 'pg-side' | 'node' | 'etcd' | 'minio' | 'infra' | 'redis' | 'app' | 'system'

export interface Component {
  id: string
  label: string
  layer: Layer
  resident: number
  typical: number
  peak: number
  detail: string[]
  /** 对应 systemd 服务名，用于和实测值对比 */
  service?: string
}

export type Level = 'ok' | 'warn' | 'danger' | 'unknown'

export interface NodeEstimate {
  ip: string
  groups: string[]
  modules: string[]
  spec: NodeSpecInput | null
  components: Component[]
  resident: number
  typical: number
  peak: number
  hugeMb: number
  level: Level
  warnings: string[]
  pg?: PgParams & { cluster: string; role: string; basedOn: string; basedOnMemMb: number }
}

type Sourced = { value: unknown; from: string }

/** 简化的有效值：schema 默认 → 全局 → 所在组（按组名顺序，与 Ansible 一致）→ 主机 */
export function hostVars(model: ConfigModel, ip: string, schema?: ParamSchema): Record<string, Sourced> {
  const out: Record<string, Sourced> = {}
  if (schema) for (const [k, d] of Object.entries(schema.params)) if (d.default !== undefined) out[k] = { value: d.default, from: '默认值' }
  const put = (vars: Vars, from: string) => {
    for (const [k, v] of Object.entries(vars)) out[k] = { value: v, from }
  }
  put(model.globalVars, 'global')
  const groups = model.groups.filter((g) => ip in g.hosts).sort((a, b) => a.name.localeCompare(b.name))
  for (const g of groups) put(g.vars, `组 ${g.name}`)
  for (const g of groups) put(g.hosts[ip], `主机 ${ip}`)
  return out
}

const val = (vars: Record<string, Sourced>, k: string, d?: unknown) => (k in vars ? vars[k].value : d)
const src = (vars: Record<string, Sourced>, k: string) => vars[k]?.from ?? '默认值'
const mb = (n: number) => `${Math.round(n).toLocaleString()} MB`

export interface EstimateInput {
  model: ConfigModel
  schema?: ParamSchema
  specs: Record<string, NodeSpecInput | undefined>
  assumptions?: Partial<Assumptions>
  baseline?: Partial<Record<string, number>>
}

export function estimateAll(input: EstimateInput): NodeEstimate[] {
  const ips = [...new Set(input.model.groups.flatMap((g) => Object.keys(g.hosts)))]
  return ips.map((ip) => estimateNode(ip, input))
}

export function estimateNode(ip: string, { model, schema, specs, assumptions, baseline }: EstimateInput): NodeEstimate {
  const a = { ...DEFAULT_ASSUMPTIONS, ...assumptions }
  const base = (id: BaselineId) => baseline?.[id] ?? BASELINE[id].mb
  const spec = specs[ip] ?? null
  const vars = hostVars(model, ip, schema)
  const groups = model.groups.filter((g) => ip in g.hosts)
  const modules = [...new Set(groups.map((g) => g.module).filter(Boolean) as string[])]
  const components: Component[] = []
  const warnings: string[] = []
  const fixed = (id: BaselineId, layer: Layer, peakOnly = false): Component => {
    const b = BASELINE[id]
    const m = base(id)
    return {
      id,
      label: b.label,
      layer,
      resident: peakOnly ? 0 : m,
      typical: peakOnly ? 0 : m,
      peak: m,
      detail: [`经验值 ${mb(m)}${baseline?.[id] !== undefined ? '（本地覆盖）' : '（待校准）'}`],
      service: b.service,
    }
  }

  // NODE 层：每台纳管节点都有
  components.push(fixed('node_exporter', 'node'), fixed('vector', 'node'), fixed('chronyd', 'node'))
  if (val(vars, 'haproxy_enabled', true) !== false) components.push(fixed('haproxy', 'node'))
  if (val(vars, 'vip_enabled', false) === true || val(vars, 'pg_vip_enabled', false) === true) components.push(fixed('vip_manager', 'node'))

  let pg: NodeEstimate['pg']
  const pgGroup = groups.find((g) => g.module === 'PGSQL')
  if (pgGroup) {
    const conf = pgConfName(val(vars, 'pg_conf'))
    const ratio = Number(val(vars, 'pg_shared_buffer_ratio', 0.25)) || 0.25
    const dest = String(val(vars, 'pg_default_service_dest', 'pgbouncer'))
    const maxConn = val(vars, 'pg_max_conn', 'auto')
    // shared_buffers 由集群主库按它自己的内存计算一次，写进 DCS 后全集群共用（DESIGN §12.1）
    const primary = Object.entries(pgGroup.hosts).find(([, hv]) => hv.pg_role === 'primary')?.[0] ?? ip
    const primarySpec = specs[primary]
    const basedOnMem = primarySpec?.memMb ?? spec?.memMb
    const role = String(val(vars, 'pg_role', 'replica'))
    if (basedOnMem) {
      const p = pgParams(basedOnMem, conf, maxConn, ratio, dest)
      pg = { ...p, cluster: pgGroup.name, role, basedOn: primary, basedOnMemMb: basedOnMem }
      const active = Math.max(1, Math.min(p.maxConnections, a.activeConn ?? 4 * (spec?.cpu ?? primarySpec?.cpu ?? 4)))
      const sbLine = `shared_buffers = ${mb(basedOnMem)} × ${ratio}（pg_shared_buffer_ratio，来源 ${src(vars, 'pg_shared_buffer_ratio')}）= ${mb(p.sharedBuffers)}`
      const confLine = `模板 ${conf}（pg_conf，来源 ${src(vars, 'pg_conf')}），max_connections = ${p.maxConnections}${maxConn === 'auto' ? `（auto，默认服务指向 ${dest}）` : ''}`
      components.push({
        id: 'pg_shared',
        label: 'PG 共享内存',
        layer: 'pg',
        resident: p.sharedBuffers + WAL_BUFFERS_MB,
        typical: p.sharedBuffers + WAL_BUFFERS_MB,
        peak: p.sharedBuffers + WAL_BUFFERS_MB,
        detail: [
          confLine,
          primary !== ip ? `按主库 ${primary} 的内存计算（${mb(basedOnMem)}）` : `按本机内存计算（${mb(basedOnMem)}）`,
          sbLine,
          `wal_buffers = ${WAL_BUFFERS_MB} MB`,
        ],
        service: 'postgresql',
      })
      components.push({ ...fixed('pg_background', 'pg'), service: undefined })
      components.push({
        id: 'pg_conn',
        label: 'PG 连接与查询',
        layer: 'pg',
        resident: 0,
        typical: active * (a.perConnMb + p.workMem),
        peak: p.maxConnections * (a.perConnMb + p.workMem * HASH_MEM_MULTIPLIER),
        detail: [
          `work_mem = clamp(floor(${mb(p.sharedBuffers)} / ${p.maxConnections}), ${TEMPLATES[conf].workMemMin}, ${TEMPLATES[conf].workMemMax}) = ${mb(p.workMem)}`,
          `典型：活跃连接 ${active} × (单连接 ${a.perConnMb} MB + work_mem ${p.workMem} MB) = ${mb(active * (a.perConnMb + p.workMem))}`,
          `上界：max_connections ${p.maxConnections} × (${a.perConnMb} MB + work_mem × hash_mem_multiplier ${HASH_MEM_MULTIPLIER}) = ${mb(p.maxConnections * (a.perConnMb + p.workMem * HASH_MEM_MULTIPLIER))}`,
        ],
      })
      const vacuum = Math.min(p.maintenanceWorkMem, 1024)
      components.push({
        id: 'pg_maint',
        label: 'PG 维护内存',
        layer: 'pg',
        resident: 0,
        typical: p.maintenanceWorkMem,
        peak: p.maintenanceWorkMem + p.autovacuumWorkers * vacuum,
        detail: [
          `maintenance_work_mem = ${mb(p.sharedBuffers)} × ${TEMPLATES[conf].maintenanceRatio} = ${mb(p.maintenanceWorkMem)}`,
          `典型：一个维护操作（建索引/VACUUM）`,
          `上界：再加 autovacuum_max_workers ${p.autovacuumWorkers} × min(maintenance_work_mem, 1GB)`,
        ],
      })
      if (primarySpec && spec && primary !== ip && spec.memMb < primarySpec.memMb * 0.95) {
        warnings.push(`本机内存（${mb(spec.memMb)}）小于主库 ${primary}（${mb(primarySpec.memMb)}），但 shared_buffers 按主库计算，有 OOM 风险`)
      }
    }
    components.push(fixed('patroni', 'pg-side'), fixed('pgbouncer', 'pg-side'), fixed('pg_exporter', 'pg-side'), fixed('pgbackrest', 'pg-side', true))
  }

  if (modules.includes('ETCD')) components.push(fixed('etcd', 'etcd'))
  if (modules.includes('MINIO')) components.push(fixed('minio', 'minio'))
  if (modules.includes('INFRA')) {
    for (const id of ['victoria_metrics', 'victoria_logs', 'grafana', 'alertmanager', 'nginx', 'blackbox_exporter', 'console'] as const)
      components.push(fixed(id, 'infra'))
  }

  for (const g of groups.filter((x) => x.module === 'REDIS')) {
    const hv = g.hosts[ip] ?? {}
    const instances = hv.redis_instances && typeof hv.redis_instances === 'object' ? Object.keys(hv.redis_instances as object) : []
    const mode = String(hv.redis_mode ?? g.vars.redis_mode ?? model.globalVars.redis_mode ?? 'standalone')
    const maxMem = parseSizeMb(hv.redis_max_memory ?? g.vars.redis_max_memory ?? model.globalVars.redis_max_memory ?? '1GB')
    const n = instances.length
    const per = mode === 'sentinel' ? base('redis_base') : maxMem * a.redisFactor + base('redis_base')
    components.push({
      id: `redis:${g.name}`,
      label: `Redis ${g.name}`,
      layer: 'redis',
      resident: n * base('redis_base'),
      typical: n * per,
      peak: n * (mode === 'sentinel' ? base('redis_base') : maxMem * 2 + base('redis_base')),
      detail:
        mode === 'sentinel'
          ? [`哨兵模式：${n} 个实例 × ${mb(base('redis_base'))}`]
          : [
              `${n} 个实例 × (redis_max_memory ${mb(maxMem)} × 系数 ${a.redisFactor} + 进程 ${mb(base('redis_base'))})`,
              `上界按 BGSAVE 写时复制翻倍计算`,
            ],
      service: 'redis',
    })
  }

  if (val(vars, 'docker_enabled', false) === true) components.push(fixed('docker_apps', 'app'))

  let hugeMb = 0
  if (spec) {
    const count = Number(val(vars, 'node_hugepage_count', 0)) || 0
    const ratio = Number(val(vars, 'node_hugepage_ratio', 0)) || 0
    hugeMb = count > 0 ? count * 2 : ratio > 0 ? spec.memMb * ratio : 0
    const reserve = Math.max(spec.memMb * a.reservePct, a.reserveMinMb)
    components.push({
      id: 'system',
      label: '系统预留',
      layer: 'system',
      resident: reserve,
      typical: reserve,
      peak: reserve,
      detail: [`max(${mb(spec.memMb)} × ${a.reservePct * 100}%, ${mb(a.reserveMinMb)}) = ${mb(reserve)}`],
    })
  }

  const sum = (k: 'resident' | 'typical' | 'peak') => components.reduce((n, c) => n + c[k], 0)
  const resident = sum('resident')
  const typical = sum('typical')
  const peak = sum('peak')

  let level: Level = 'unknown'
  if (spec) {
    level = typical > spec.memMb * 0.9 || resident > spec.memMb * 0.75 ? 'danger' : typical > spec.memMb * 0.75 ? 'warn' : 'ok'
    if (hugeMb > 0) {
      if (pg && pg.sharedBuffers > hugeMb) warnings.push(`已开启大页（${mb(hugeMb)}），但 shared_buffers（${mb(pg.sharedBuffers)}）放不进大页`)
      const nonPg = typical - (pg ? pg.sharedBuffers + WAL_BUFFERS_MB : 0)
      if (spec.memMb - hugeMb < nonPg) warnings.push(`大页预留 ${mb(hugeMb)} 后，剩余内存不足以容纳其他组件（约需 ${mb(nonPg)}）`)
    }
    if (pg && pg.basedOn !== ip && !specs[pg.basedOn]) warnings.push(`主库 ${pg.basedOn} 规格未知，暂按本机内存计算`)
  }
  if (!spec) warnings.push('节点规格未知：没有监控指标，也没有填写规划规格')

  return { ip, groups: groups.map((g) => g.name), modules, spec, components, resident, typical, peak, hugeMb, level, warnings, pg }
}
