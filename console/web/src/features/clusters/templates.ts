// 新建集群向导生成的配置片段（参考 conf/ha/trio.yml、conf/ha/full.yml 的写法）

export const NAME_RE = /^[a-z][a-z0-9-]{0,62}$/
export const IPV4 = /^(25[0-5]|2[0-4]\d|1?\d?\d)(\.(25[0-5]|2[0-4]\d|1?\d?\d)){3}$/

export function randomPassword(len = 20) {
  const chars = 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789'
  const buf = new Uint32Array(len)
  crypto.getRandomValues(buf)
  return [...buf].map((n) => chars[n % chars.length]).join('')
}

// ---------- PostgreSQL ----------

export interface PgInstance {
  ip: string
  role: 'primary' | 'replica' | 'offline'
}

export interface PgUser {
  name: string
  password: string
  role: 'dbrole_readwrite' | 'dbrole_readonly' | 'dbrole_admin' | 'dbrole_offline'
  pgbouncer: boolean
  comment: string
}

export interface PgDatabase {
  name: string
  owner: string
  comment: string
}

export interface PgSpec {
  name: string
  version: string // 空 = 使用全局 pg_version
  conf: string // 空 = 使用全局 pg_conf
  instances: PgInstance[]
  users: PgUser[]
  databases: PgDatabase[]
  vip: string // 空 = 不启用
  vipInterface: string
}

export function pgGroup(s: PgSpec): Record<string, unknown> {
  const hosts = Object.fromEntries(
    s.instances.map((ins, i) => [
      ins.ip,
      ins.role === 'offline' ? { pg_seq: i + 1, pg_role: 'replica', pg_offline_query: true } : { pg_seq: i + 1, pg_role: ins.role },
    ]),
  )
  const vars: Record<string, unknown> = { pg_cluster: s.name }
  if (s.version) vars.pg_version = Number(s.version)
  if (s.conf) vars.pg_conf = s.conf
  if (s.users.length)
    vars.pg_users = s.users.map((u) => ({
      name: u.name,
      password: u.password,
      pgbouncer: u.pgbouncer,
      roles: [u.role],
      ...(u.comment ? { comment: u.comment } : {}),
    }))
  if (s.databases.length)
    vars.pg_databases = s.databases.map((d) => ({
      name: d.name,
      ...(d.owner ? { owner: d.owner } : {}),
      ...(d.comment ? { comment: d.comment } : {}),
    }))
  if (s.vip) {
    vars.pg_vip_enabled = true
    vars.pg_vip_address = s.vip.includes('/') ? s.vip : `${s.vip}/24`
    vars.pg_vip_interface = s.vipInterface || 'eth0'
  }
  return { hosts, vars }
}

export function validatePg(s: PgSpec, existingGroups: string[]): string[] {
  const errs: string[] = []
  if (!NAME_RE.test(s.name)) errs.push('集群名只能包含小写字母、数字和连字符，且以字母开头')
  if (existingGroups.includes(s.name)) errs.push(`配置中已存在名为 ${s.name} 的组`)
  if (!s.instances.length) errs.push('至少需要一个实例')
  const ips = s.instances.map((i) => i.ip)
  if (ips.some((ip) => !IPV4.test(ip))) errs.push('存在不合法的 IP 地址')
  if (new Set(ips).size !== ips.length) errs.push('同一个 IP 不能出现两次（一机多实例请在工作台手工配置 pg_instances）')
  if (s.instances.filter((i) => i.role === 'primary').length !== 1) errs.push('必须且只能有一个主库（primary）')
  for (const u of s.users) if (!/^[a-z_][a-z0-9_]*$/.test(u.name)) errs.push(`用户名 ${u.name || '（空）'} 不合法`)
  for (const d of s.databases) if (!/^[a-z_][a-z0-9_]*$/.test(d.name)) errs.push(`库名 ${d.name || '（空）'} 不合法`)
  if (s.vip && !IPV4.test(s.vip.split('/')[0])) errs.push('VIP 地址不合法')
  return errs
}

// ---------- Redis ----------

export type RedisMode = 'standalone' | 'sentinel' | 'cluster'

export interface RedisNode {
  ip: string
  ports: number[]
  /** standalone 模式：每个端口的上游（"ip port"），空 = 主实例 */
  replicaOf: Record<number, string>
}

export interface SentinelMonitor {
  name: string
  host: string
  port: number
  password: string
  quorum: number
}

export interface RedisSpec {
  name: string
  mode: RedisMode
  maxMemory: string
  password: string
  nodes: RedisNode[]
  clusterReplicas: number
  monitors: SentinelMonitor[]
}

export function redisGroup(s: RedisSpec): Record<string, unknown> {
  const hosts = Object.fromEntries(
    s.nodes.map((n, i) => [
      n.ip,
      {
        redis_node: i + 1,
        redis_instances: Object.fromEntries(
          n.ports.map((p) => [p, s.mode === 'standalone' && n.replicaOf[p] ? { replica_of: n.replicaOf[p] } : {}]),
        ),
      },
    ]),
  )
  const vars: Record<string, unknown> = { redis_cluster: s.name }
  if (s.mode !== 'standalone') vars.redis_mode = s.mode
  if (s.password) vars.redis_password = s.password
  if (s.maxMemory) vars.redis_max_memory = s.maxMemory
  if (s.mode === 'cluster' && s.clusterReplicas !== 1) vars.redis_cluster_replicas = s.clusterReplicas
  if (s.mode === 'sentinel' && s.monitors.length)
    vars.redis_sentinel_monitor = s.monitors.map((m) => ({
      name: m.name,
      host: m.host,
      port: m.port,
      ...(m.password ? { password: m.password } : {}),
      quorum: m.quorum,
    }))
  return { hosts, vars }
}

export function validateRedis(s: RedisSpec, existingGroups: string[]): string[] {
  const errs: string[] = []
  if (!NAME_RE.test(s.name)) errs.push('集群名只能包含小写字母、数字和连字符，且以字母开头')
  if (existingGroups.includes(s.name)) errs.push(`配置中已存在名为 ${s.name} 的组`)
  if (!s.nodes.length) errs.push('至少需要一个节点')
  if (s.nodes.some((n) => !IPV4.test(n.ip))) errs.push('存在不合法的 IP 地址')
  if (new Set(s.nodes.map((n) => n.ip)).size !== s.nodes.length) errs.push('同一个 IP 不能出现两次')
  if (s.nodes.some((n) => !n.ports.length)) errs.push('每个节点至少需要一个实例端口')
  if (s.nodes.some((n) => n.ports.some((p) => !(p > 0 && p < 65536)))) errs.push('端口不合法')
  if (!/^\d+(\.\d+)?\s*(KB|MB|GB)$/i.test(s.maxMemory)) errs.push('最大内存格式应为 64MB / 1GB 这样的写法')
  if (s.mode === 'cluster') {
    const total = s.nodes.reduce((n, x) => n + x.ports.length, 0)
    const need = 3 * (1 + s.clusterReplicas)
    if (total < need) errs.push(`原生集群模式至少需要 ${need} 个实例（3 主 × 每主 ${s.clusterReplicas} 从），当前 ${total} 个`)
  }
  if (s.mode === 'sentinel') {
    const total = s.nodes.reduce((n, x) => n + x.ports.length, 0)
    if (total < 3) errs.push('哨兵至少需要 3 个实例才能形成多数派')
    for (const m of s.monitors) if (!m.name || !IPV4.test(m.host)) errs.push(`监控目标 ${m.name || '（未命名）'} 填写不完整`)
  }
  if (s.mode === 'standalone') {
    const all = new Set(s.nodes.flatMap((n) => n.ports.map((p) => `${n.ip} ${p}`)))
    for (const n of s.nodes) for (const [p, up] of Object.entries(n.replicaOf)) if (up && !all.has(up)) errs.push(`${n.ip}:${p} 的上游 ${up} 不在本集群中`)
  }
  return errs
}
