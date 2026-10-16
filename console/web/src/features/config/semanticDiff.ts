import { buildModel, isSensitiveKey, type Vars } from './model'

export interface Change {
  kind: 'add' | 'remove' | 'change'
  scope: string // 如「全局」「组 pg-meta」「pg-meta / 10.10.10.10」
  key?: string // 变量名；为空表示组或主机本身
  before?: unknown
  after?: unknown
  sensitive: boolean
}

const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b)

function diffVars(scope: string, a: Vars, b: Vars, out: Change[]) {
  for (const k of new Set([...Object.keys(a), ...Object.keys(b)])) {
    const sensitive = isSensitiveKey(k)
    if (!(k in a)) out.push({ kind: 'add', scope, key: k, after: b[k], sensitive })
    else if (!(k in b)) out.push({ kind: 'remove', scope, key: k, before: a[k], sensitive })
    else if (!same(a[k], b[k])) out.push({ kind: 'change', scope, key: k, before: a[k], after: b[k], sensitive })
  }
}

/** 按 inventory 语义比较两份配置：全局变量、组、组变量、主机、主机变量 */
export function semanticDiff(beforeJs: unknown, afterJs: unknown): Change[] {
  const a = buildModel(beforeJs)
  const b = buildModel(afterJs)
  const out: Change[] = []
  diffVars('全局', a.globalVars, b.globalVars, out)

  const ga = new Map(a.groups.map((g) => [g.name, g]))
  const gb = new Map(b.groups.map((g) => [g.name, g]))
  for (const name of new Set([...ga.keys(), ...gb.keys()])) {
    const x = ga.get(name)
    const y = gb.get(name)
    if (!x) {
      out.push({ kind: 'add', scope: `组 ${name}`, after: `${Object.keys(y!.hosts).length} 台主机`, sensitive: false })
      continue
    }
    if (!y) {
      out.push({ kind: 'remove', scope: `组 ${name}`, before: `${Object.keys(x.hosts).length} 台主机`, sensitive: false })
      continue
    }
    diffVars(`组 ${name}`, x.vars, y.vars, out)
    for (const ip of new Set([...Object.keys(x.hosts), ...Object.keys(y.hosts)])) {
      const scope = `${name} / ${ip}`
      if (!(ip in x.hosts)) out.push({ kind: 'add', scope, after: y.hosts[ip], sensitive: false })
      else if (!(ip in y.hosts)) out.push({ kind: 'remove', scope, before: x.hosts[ip], sensitive: false })
      else diffVars(scope, x.hosts[ip], y.hosts[ip], out)
    }
  }
  return out
}

export function summarize(changes: Change[]): string {
  if (changes.length === 0) return '无语义变化（仅注释或格式）'
  const parts = changes.slice(0, 3).map((c) => {
    const verb = c.kind === 'add' ? '新增' : c.kind === 'remove' ? '删除' : '修改'
    return c.key ? `${verb} ${c.scope} ${c.key}` : `${verb} ${c.scope}`
  })
  return parts.join('；') + (changes.length > 3 ? ` 等 ${changes.length} 处` : '')
}

export function preview(v: unknown, sensitive: boolean): string {
  if (v === undefined) return ''
  if (sensitive) return '******'
  const s = typeof v === 'string' ? v : JSON.stringify(v)
  return s.length > 120 ? s.slice(0, 117) + '...' : s
}
