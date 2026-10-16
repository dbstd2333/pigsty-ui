import type { InventoryModule } from '@/api/types'
import type { Path } from './yamlPatch'

export type Vars = Record<string, unknown>

export interface GroupModel {
  name: string
  module: InventoryModule | null
  vars: Vars
  hosts: Record<string, Vars>
}

export interface ConfigModel {
  globalVars: Vars
  groups: GroupModel[]
}

const isObj = (v: unknown): v is Record<string, unknown> => typeof v === 'object' && v !== null && !Array.isArray(v)

// 与 bin/validate、后端保持一致的模块识别规则
export function detectModule(name: string, vars: Vars): InventoryModule | null {
  if ('pg_cluster' in vars) return 'PGSQL'
  if ('etcd_cluster' in vars) return 'ETCD'
  if ('minio_cluster' in vars) return 'MINIO'
  if ('redis_cluster' in vars) return 'REDIS'
  if (name === 'infra') return 'INFRA'
  if ('docker_enabled' in vars || 'apps' in vars) return 'APP'
  return null
}

export function buildModel(js: unknown): ConfigModel {
  const all = isObj(js) && isObj(js.all) ? js.all : {}
  const children = isObj(all.children) ? all.children : {}
  const groups = Object.entries(children).map(([name, g]) => {
    const gv = isObj(g) && isObj(g.vars) ? g.vars : {}
    const hosts = isObj(g) && isObj(g.hosts) ? g.hosts : {}
    return {
      name,
      module: detectModule(name, gv),
      vars: gv,
      hosts: Object.fromEntries(Object.entries(hosts).map(([ip, hv]) => [ip, isObj(hv) ? hv : {}])),
    }
  })
  return { globalVars: isObj(all.vars) ? all.vars : {}, groups }
}

export function allHosts(model: ConfigModel): string[] {
  return [...new Set(model.groups.flatMap((g) => Object.keys(g.hosts)))].sort(compareIp)
}

export function compareIp(a: string, b: string) {
  const pa = a.split('.').map(Number)
  const pb = b.split('.').map(Number)
  for (let i = 0; i < 4; i++) if (pa[i] !== pb[i]) return (pa[i] || 0) - (pb[i] || 0)
  return a.localeCompare(b)
}

/** 工作台中选中的作用域 */
export type Scope = { kind: 'global' } | { kind: 'group'; group: string } | { kind: 'host'; group: string; host: string }

export function scopeKey(s: Scope) {
  return s.kind === 'global' ? 'global' : s.kind === 'group' ? `group:${s.group}` : `host:${s.group}:${s.host}`
}

export function parseScopeKey(key: string): Scope {
  if (key.startsWith('group:')) return { kind: 'group', group: key.slice(6) }
  if (key.startsWith('host:')) {
    const rest = key.slice(5)
    const i = rest.lastIndexOf(':')
    return { kind: 'host', group: rest.slice(0, i), host: rest.slice(i + 1) }
  }
  return { kind: 'global' }
}

/** 作用域下变量所在的 YAML 路径 */
export function scopePath(s: Scope): Path {
  if (s.kind === 'global') return ['all', 'vars']
  if (s.kind === 'group') return ['all', 'children', s.group, 'vars']
  return ['all', 'children', s.group, 'hosts', s.host]
}

export function scopeVars(model: ConfigModel, s: Scope): Vars {
  if (s.kind === 'global') return model.globalVars
  const g = model.groups.find((x) => x.name === s.group)
  if (!g) return {}
  return s.kind === 'group' ? g.vars : (g.hosts[s.host] ?? {})
}

export function scopeLabel(s: Scope) {
  return s.kind === 'global' ? '全局变量' : s.kind === 'group' ? `组 ${s.group}` : `${s.group} / ${s.host}`
}

const SENSITIVE = /password|secret|_key$|_pass$|cipher_pass/i

export function isSensitiveKey(key: string) {
  return SENSITIVE.test(key)
}

export const MODULE_COLORS: Record<InventoryModule, string> = {
  INFRA: 'purple',
  ETCD: 'gold',
  MINIO: 'cyan',
  PGSQL: 'blue',
  REDIS: 'red',
  APP: 'green',
}
