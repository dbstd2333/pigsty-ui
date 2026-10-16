/**
 * 前端即时校验：移植 bin/validate 的主要规则，并补充若干交叉引用与安全提示。
 * 后端保存前还会再跑一遍 bin/validate，那才是权威结果。
 */
import type { Document } from 'yaml'
import type { Issue, ParamSchema } from '@/api/types'
import type { ConfigModel, Vars } from './model'
import { lineOf, type Path } from './yamlPatch'

const IPV4 = /^(\d{1,3}\.){3}\d{1,3}$/
const IPV4_CIDR = /^(\d{1,3}\.){3}\d{1,3}\/\d{1,2}$/
const PG_ROLES = ['primary', 'replica', 'standby', 'offline', 'delayed']

// 与 schema 默认值相同即视为默认密码
const PASSWORD_KEYS = [
  'grafana_admin_password',
  'pg_admin_password',
  'pg_monitor_password',
  'pg_replication_password',
  'patroni_password',
  'haproxy_admin_password',
  'minio_secret_key',
  'etcd_root_password',
]

export function runRules(text: string, doc: Document.Parsed, model: ConfigModel, schema?: ParamSchema): Issue[] {
  const issues: Issue[] = []
  const add = (level: Issue['level'], message: string, path?: Path, source: Issue['source'] = 'semantic') =>
    issues.push({ level, source, message, path: path?.join('.'), line: path ? lineOf(text, doc, path) : undefined })

  // YAML 语法
  for (const e of doc.errors) {
    issues.push({ level: 'error', source: 'yaml', message: e.message.split('\n')[0], line: e.linePos?.[0].line, column: e.linePos?.[0].col })
  }
  if (doc.errors.length) return issues

  const js = doc.toJS() as { all?: { children?: unknown; vars?: unknown } } | null
  if (!js?.all) {
    add('error', '缺少顶层 all')
    return issues
  }
  if (!js.all.children || typeof js.all.children !== 'object') add('error', '缺少 all.children', ['all'])

  // 全局变量
  const gv = model.globalVars
  const gp = (k: string): Path => ['all', 'vars', k]
  if (!('admin_ip' in gv)) add('error', '缺少 all.vars.admin_ip', ['all', 'vars'])
  else if (!IPV4.test(String(gv.admin_ip))) add('error', 'admin_ip 不是合法的 IPv4 地址', gp('admin_ip'))
  if (!('version' in gv)) add('warning', '缺少 all.vars.version，这是 Pigsty 配置文件吗？', ['all', 'vars'])
  if ('region' in gv && !['default', 'china', 'europe'].includes(String(gv.region)))
    add('warning', `region 取值 ${String(gv.region)} 不在 default/china/europe 中`, gp('region'))
  if ('minio_cluster' in gv) add('error', 'minio_cluster 不能定义在全局变量中，请放到 minio 组的 vars', gp('minio_cluster'))

  // 组与主机
  const memberOf: Record<string, Record<string, string[]>> = {}
  for (const g of model.groups) {
    const base: Path = ['all', 'children', g.name]
    if (Object.keys(g.hosts).length === 0) add('error', `组 ${g.name} 没有主机`, base)
    for (const ip of Object.keys(g.hosts)) {
      if (!IPV4.test(ip)) add('error', `组 ${g.name} 中的主机 ${ip} 不是合法的 IPv4 地址`, [...base, 'hosts', ip])
      if (g.module) ((memberOf[g.module] ??= {})[ip] ??= []).push(g.name)
    }
    checkSeq(g.name, g.hosts, g.module, add)
    if (g.module === 'PGSQL') checkPgsql(g.name, g.vars, g.hosts, add)
  }
  for (const [mod, hosts] of Object.entries(memberOf)) {
    for (const [ip, groups] of Object.entries(hosts)) {
      if (groups.length > 1) add('error', `主机 ${ip} 同时属于多个 ${mod} 组：${groups.join(', ')}`)
    }
  }

  const hasPg = model.groups.some((g) => g.module === 'PGSQL')
  if (hasPg && !model.groups.some((g) => g.name === 'etcd')) add('warning', '存在 PG 集群但没有 etcd 组，Patroni 需要 etcd')
  const etcd = model.groups.find((g) => g.name === 'etcd')
  if (etcd && Object.keys(etcd.hosts).length % 2 === 0)
    add('warning', `etcd 节点数为 ${Object.keys(etcd.hosts).length}，建议使用奇数个节点`, ['all', 'children', 'etcd', 'hosts'])

  const method = gv.pgbackrest_method
  if (method === 'minio' && !model.groups.some((g) => g.module === 'MINIO'))
    add('warning', 'pgbackrest_method 为 minio，但没有定义 minio 组', gp('pgbackrest_method'))
  if (typeof method === 'string' && gv.pgbackrest_repo && typeof gv.pgbackrest_repo === 'object' && !(method in gv.pgbackrest_repo))
    add('error', `pgbackrest_method = ${method}，但 pgbackrest_repo 中没有该仓库`, gp('pgbackrest_method'))

  // 安全提示
  if (schema) {
    for (const k of PASSWORD_KEYS) {
      const def = schema.params[k]?.default
      const v = k in gv ? gv[k] : def
      if (def !== undefined && v === def) add('warning', `${k} 仍是默认值，生产环境请修改（可用 ./configure -g 生成）`, k in gv ? gp(k) : undefined)
    }
  }
  const ports = gv.node_firewall_public_port
  if (Array.isArray(ports) && ports.includes(5432))
    add('warning', 'node_firewall_public_port 对公网开放了 5432，生产环境请移除', gp('node_firewall_public_port'))

  // 未知参数
  if (schema) {
    const known = (k: string) => k in schema.params
    const scan = (vars: Vars, path: Path, skip: Set<string>) => {
      for (const k of Object.keys(vars)) if (!known(k) && !skip.has(k)) add('warning', `未知参数 ${k}（拼写错误？）`, [...path, k], 'schema')
    }
    const ident = new Set(['pg_seq', 'pg_role', 'pg_upstream', 'pg_instances', 'infra_seq', 'etcd_seq', 'minio_seq', 'redis_node', 'redis_instances', 'nodename', 'app', 'apps', 'ansible_host', 'ansible_user', 'ansible_port'])
    scan(gv, ['all', 'vars'], ident)
    for (const g of model.groups) {
      scan(g.vars, ['all', 'children', g.name, 'vars'], ident)
      for (const [ip, hv] of Object.entries(g.hosts)) scan(hv, ['all', 'children', g.name, 'hosts', ip], ident)
    }
  }

  return issues
}

type Add = (level: Issue['level'], message: string, path?: Path) => void

const SEQ_KEY: Record<string, string> = { INFRA: 'infra_seq', ETCD: 'etcd_seq', MINIO: 'minio_seq', PGSQL: 'pg_seq' }

function checkSeq(group: string, hosts: Record<string, Vars>, module: string | null, add: Add) {
  const key = module && SEQ_KEY[module]
  if (!key) return
  const seen = new Map<number, string>()
  for (const [ip, hv] of Object.entries(hosts)) {
    const p: Path = ['all', 'children', group, 'hosts', ip]
    const v = hv[key]
    if (v === undefined) add('error', `${group} / ${ip} 缺少 ${key}`, p)
    else if (!Number.isInteger(v) || (v as number) < 0) add('error', `${group} / ${ip} 的 ${key} 应为非负整数`, [...p, key])
    else if (seen.has(v as number)) add('error', `${group} 中 ${key} = ${v} 重复（${seen.get(v as number)} 与 ${ip}）`, [...p, key])
    else seen.set(v as number, ip)
  }
}

function checkPgsql(group: string, vars: Vars, hosts: Record<string, Vars>, add: Add) {
  const base: Path = ['all', 'children', group, 'vars']
  const cls = String(vars.pg_cluster ?? '')
  if (cls !== group) add('warning', `pg_cluster (${cls}) 与组名 ${group} 不一致`, [...base, 'pg_cluster'])
  if (!/^[a-zA-Z0-9-]+$/.test(cls)) add('error', `pg_cluster 名称 ${cls} 只能包含字母、数字和 -`, [...base, 'pg_cluster'])

  let primaries = 0
  for (const [ip, hv] of Object.entries(hosts)) {
    const p: Path = ['all', 'children', group, 'hosts', ip]
    if (!('pg_role' in hv)) add('error', `${group} / ${ip} 缺少 pg_role`, p)
    else if (!PG_ROLES.includes(String(hv.pg_role))) add('error', `${group} / ${ip} 的 pg_role 应为 ${PG_ROLES.join('/')}`, [...p, 'pg_role'])
    if (hv.pg_role === 'primary') primaries++
    if ('pg_upstream' in hv && !IPV4.test(String(hv.pg_upstream))) add('error', `${group} / ${ip} 的 pg_upstream 不是合法 IPv4`, [...p, 'pg_upstream'])
  }
  if (primaries === 0) add('error', `集群 ${group} 没有 primary 实例`, ['all', 'children', group, 'hosts'])
  if (primaries > 1) add('error', `集群 ${group} 有 ${primaries} 个 primary 实例`, ['all', 'children', group, 'hosts'])

  // 业务对象
  const users = Array.isArray(vars.pg_users) ? (vars.pg_users as Vars[]) : []
  const userNames = new Set<string>()
  users.forEach((u, i) => {
    if (!u?.name) add('error', `${group} 的 pg_users[${i}] 缺少 name`, [...base, 'pg_users'])
    else if (userNames.has(String(u.name))) add('error', `${group} 中用户 ${u.name} 重复定义`, [...base, 'pg_users'])
    else userNames.add(String(u.name))
  })
  const dbs = Array.isArray(vars.pg_databases) ? (vars.pg_databases as Vars[]) : []
  const dbNames = new Set<string>()
  dbs.forEach((d, i) => {
    if (!d?.name) add('error', `${group} 的 pg_databases[${i}] 缺少 name`, [...base, 'pg_databases'])
    else if (dbNames.has(String(d.name))) add('error', `${group} 中数据库 ${d.name} 重复定义`, [...base, 'pg_databases'])
    else dbNames.add(String(d.name))
    // owner 可能是系统用户（如 dbuser_dba），只给警告
    if (d?.owner && !userNames.has(String(d.owner)) && !String(d.owner).startsWith('dbuser_dba') && d.owner !== 'postgres')
      add('warning', `${group} 数据库 ${d.name} 的 owner ${d.owner} 不在 pg_users 中`, [...base, 'pg_databases'])
  })
  const hba = Array.isArray(vars.pg_hba_rules) ? (vars.pg_hba_rules as Vars[]) : []
  hba.forEach((r, i) => {
    if (!r || typeof r !== 'object') add('error', `${group} 的 pg_hba_rules[${i}] 不是对象`, [...base, 'pg_hba_rules'])
    else if (!('rules' in r) && !(r.user && r.db && r.addr && r.auth))
      add('error', `${group} 的 pg_hba_rules[${i}] 需要 user/db/addr/auth，或使用 rules 原始写法`, [...base, 'pg_hba_rules'])
  })
  if (vars.pg_vip_enabled === true) {
    const addr = String(vars.pg_vip_address ?? '')
    if (!IPV4_CIDR.test(addr)) add('error', `${group} 开启了 VIP，但 pg_vip_address 不是 x.x.x.x/nn 格式`, [...base, 'pg_vip_address'])
    else if (Object.keys(hosts).includes(addr.split('/')[0])) add('error', `${group} 的 VIP 与节点 IP 冲突`, [...base, 'pg_vip_address'])
  }
}
