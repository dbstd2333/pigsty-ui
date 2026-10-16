// 「已应用版本 → 当前配置」的语义变化推导建议动作（DESIGN §10）
import type { ParamSchema } from '@/api/types'
import type { ConfigModel, GroupModel, Vars } from '@/features/config/model'

export interface Suggestion {
  key: string
  action: string
  params: Record<string, unknown>
  reason: string
}

export interface PlanNote {
  level: 'info' | 'warning'
  message: string
}

export interface ClusterPlan {
  changed: boolean
  suggestions: Suggestion[]
  notes: PlanNote[]
}

const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b)

/** 集群视角的变量：全局 + 组（组覆盖全局） */
const clusterVars = (m: ConfigModel, g: GroupModel | undefined): Vars => (g ? { ...m.globalVars, ...g.vars } : {})

function byName(v: unknown): Map<string, unknown> {
  const out = new Map<string, unknown>()
  if (Array.isArray(v)) for (const x of v) if (x && typeof x === 'object' && 'name' in x) out.set(String((x as { name: unknown }).name), x)
  return out
}

const PG_PARAM_KEYS = ['pg_conf', 'pg_shared_buffer_ratio', 'pg_max_conn']

export function planCluster(cluster: string, before: ConfigModel | undefined, after: ConfigModel, schema?: ParamSchema): ClusterPlan {
  const ga = before?.groups.find((g) => g.name === cluster)
  const gb = after.groups.find((g) => g.name === cluster)
  const suggestions: Suggestion[] = []
  const notes: PlanNote[] = []
  const add = (s: Omit<Suggestion, 'key'>) => suggestions.push({ ...s, key: `${s.action}:${JSON.stringify(s.params)}` })

  if (!gb) return { changed: !!ga, suggestions, notes: ga ? [{ level: 'warning', message: '集群已从配置中删除；如集群仍在运行，需要手动移除（删除整集群不在 console 动作范围内）' }] : [] }

  const isRedis = gb.module === 'REDIS'
  const knownIps = new Set(before ? before.groups.flatMap((g) => Object.keys(g.hosts)) : [])
  const hostsB = Object.keys(gb.hosts)

  // 新集群
  if (!ga) {
    const fresh = hostsB.filter((ip) => !knownIps.has(ip))
    if (fresh.length) add({ action: 'node-add', params: { ips: fresh }, reason: `新节点 ${fresh.join('、')} 需要先纳管` })
    if (isRedis) add({ action: 'redis-create', params: { cluster }, reason: '新建 Redis 集群' })
    else if (gb.module === 'PGSQL') add({ action: 'pgsql-create', params: { cluster }, reason: '新建 PostgreSQL 集群' })
    return { changed: true, suggestions, notes }
  }

  const va = clusterVars(before!, ga)
  const vb = clusterVars(after, gb)
  const changedKeys = [...new Set([...Object.keys(va), ...Object.keys(vb)])].filter((k) => !same(va[k], vb[k]))

  // 主机增减
  const hostsA = Object.keys(ga.hosts)
  const added = hostsB.filter((ip) => !hostsA.includes(ip))
  const removed = hostsA.filter((ip) => !hostsB.includes(ip))
  const hostVarChanged = hostsB.filter((ip) => hostsA.includes(ip) && !same(ga.hosts[ip], gb.hosts[ip]))

  if (added.length) {
    const fresh = added.filter((ip) => !knownIps.has(ip))
    if (fresh.length) add({ action: 'node-add', params: { ips: fresh }, reason: `新节点 ${fresh.join('、')} 需要先纳管` })
    if (isRedis) add({ action: 'redis-create', params: { cluster }, reason: `新增 Redis 节点 ${added.join('、')}` })
    else add({ action: 'pgsql-add-replica', params: { cluster, ips: added }, reason: `新增实例 ${added.join('、')}` })
  }
  if (removed.length) {
    notes.push({
      level: 'warning',
      message: `配置中删除了 ${removed.join('、')}。移除实例必须在删除配置之前执行（playbook 依赖 inventory）：请先恢复这些主机的配置，在集群页对实例执行「移除实例」，成功后会自动删除配置。`,
    })
  }
  if (hostVarChanged.length) {
    const roleChanged = hostVarChanged.filter((ip) => ga.hosts[ip].pg_role !== gb.hosts[ip].pg_role)
    if (roleChanged.length)
      notes.push({ level: 'info', message: `${roleChanged.join('、')} 的 pg_role 有变化：pg_role 只影响初始化与服务路由，主从切换请用「主从切换」动作。` })
    if (hostVarChanged.length > roleChanged.length) notes.push({ level: 'info', message: `${hostVarChanged.length - roleChanged.length} 台主机的实例级变量有变化，可能需要重新执行对应步骤。` })
  }

  if (isRedis) {
    if (changedKeys.length) add({ action: 'redis-create', params: { cluster }, reason: `Redis 配置变化：${changedKeys.join('、')}（重新执行 redis.yml 会重启实例）` })
    return { changed: true, suggestions, notes }
  }

  const handled = new Set<string>()
  const take = (keys: string[]) => keys.filter((k) => changedKeys.includes(k) && (handled.add(k), true))

  if (take(['pg_users']).length) {
    const a = byName(va.pg_users)
    const b = byName(vb.pg_users)
    for (const [name, u] of b) if (!same(a.get(name), u)) add({ action: 'pgsql-user', params: { cluster, user: name }, reason: a.has(name) ? `用户 ${name} 定义有变化` : `新增用户 ${name}` })
    for (const name of a.keys()) if (!b.has(name)) notes.push({ level: 'warning', message: `用户 ${name} 已从配置删除，但数据库中仍存在。要删除用户，请改为 state: absent 后执行。` })
  }
  if (take(['pg_databases']).length) {
    const a = byName(va.pg_databases)
    const b = byName(vb.pg_databases)
    for (const [name, d] of b) if (!same(a.get(name), d)) add({ action: 'pgsql-db', params: { cluster, db: name }, reason: a.has(name) ? `数据库 ${name} 定义有变化` : `新增数据库 ${name}` })
    for (const name of a.keys()) if (!b.has(name)) notes.push({ level: 'warning', message: `数据库 ${name} 已从配置删除，但实际仍存在。要删除，请改为 state: absent 后执行。` })
  }
  const hba = take(['pg_hba_rules', 'pgb_hba_rules', 'pg_default_hba_rules', 'pgb_default_hba_rules'])
  if (hba.length) add({ action: 'pgsql-hba', params: { cluster }, reason: `${hba.join('、')} 有变化` })
  const svc = take(['pg_services', 'pg_default_services', 'pg_vip_enabled', 'pg_vip_address'])
  if (svc.length) add({ action: 'pgsql-svc', params: { cluster }, reason: `${svc.join('、')} 有变化` })
  const ext = take(['pg_extensions', 'pg_packages'])
  if (ext.length) add({ action: 'pgsql-ext', params: { cluster }, reason: `${ext.join('、')} 有变化（只安装软件包，CREATE EXTENSION 在数据库定义里）` })
  if (take(['pg_crontab']).length) add({ action: 'pgsql-crontab', params: { cluster }, reason: 'pg_crontab 有变化' })
  if (take(['pg_parameters']).length) {
    const a = (va.pg_parameters ?? {}) as Record<string, unknown>
    const b = (vb.pg_parameters ?? {}) as Record<string, unknown>
    const settings = Object.fromEntries(Object.keys(b).filter((k) => !same(a[k], b[k])).map((k) => [k, String(b[k])]))
    if (Object.keys(settings).length) add({ action: 'patroni-param', params: { cluster, settings }, reason: `pg_parameters 有变化：${Object.keys(settings).join('、')}` })
  }
  const memKeys = take(PG_PARAM_KEYS)
  if (memKeys.length)
    notes.push({
      level: 'warning',
      message: `${memKeys.join('、')} 只在集群初始化时生效（参数已写入 DCS）。对已运行的集群，请用「修改 Patroni 参数」直接调整 shared_buffers、max_connections 等。`,
    })

  for (const k of changedKeys) {
    if (handled.has(k)) continue
    const impact = schema?.params[k]?.impact
    if (impact === 'init-only') notes.push({ level: 'warning', message: `${k} 只在初始化时生效，对已有集群不会生效` })
    else notes.push({ level: 'info', message: `${k} 有变化，没有对应动作，请参考文档手动处理` })
  }

  return { changed: changedKeys.length > 0 || added.length > 0 || removed.length > 0 || hostVarChanged.length > 0, suggestions, notes }
}
