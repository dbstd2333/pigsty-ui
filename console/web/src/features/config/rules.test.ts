import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import type { ParamSchema } from '@/api/types'
import { buildModel } from './model'
import { runRules } from './rules'
import { semanticDiff } from './semanticDiff'
import { parse, setIn } from './yamlPatch'

const ROOT = join(import.meta.dirname, '../../../../..')
const pigsty = readFileSync(join(ROOT, 'pigsty.yml'), 'utf8')
const schema = JSON.parse(readFileSync(join(ROOT, 'console/schema/v4.5.0.json'), 'utf8')) as ParamSchema

function check(text: string) {
  const doc = parse(text)
  return runRules(text, doc, buildModel(doc.toJS()), schema)
}

describe('runRules', () => {
  it('默认 pigsty.yml 没有错误，只有安全提示', () => {
    const issues = check(pigsty)
    expect(issues.filter((i) => i.level === 'error')).toEqual([])
    expect(issues.some((i) => i.message.includes('pg_admin_password 仍是默认值'))).toBe(true)
    expect(issues.some((i) => i.message.includes('5432'))).toBe(true)
    expect(issues.filter((i) => i.message.startsWith('未知参数'))).toEqual([])
  })

  it('两个 primary 报错并定位到行', () => {
    let t = setIn(pigsty, ['all', 'children', 'pg-meta', 'hosts', '10.10.10.11'], { pg_seq: 2, pg_role: 'primary' })
    t = setIn(t, ['all', 'children', 'pg-meta', 'hosts', '10.10.10.11', 'pg_role'], 'primary')
    const issues = check(t)
    const e = issues.find((i) => i.message.includes('2 个 primary'))
    expect(e?.level).toBe('error')
    expect(e?.line).toBeGreaterThan(0)
  })

  it('pg_seq 重复', () => {
    const t = setIn(pigsty, ['all', 'children', 'pg-meta', 'hosts', '10.10.10.11'], { pg_seq: 1, pg_role: 'replica' })
    expect(check(t).some((i) => i.level === 'error' && i.message.includes('pg_seq = 1 重复'))).toBe(true)
  })

  it('YAML 语法错误', () => {
    const issues = check(pigsty.replace('pg_version: 18', 'pg_version: [18'))
    expect(issues[0].source).toBe('yaml')
    expect(issues[0].line).toBeGreaterThan(0)
  })

  it('pgbackrest_method 指向不存在的仓库', () => {
    const t = setIn(pigsty, ['all', 'vars', 'pgbackrest_method'], 'oss')
    const t2 = setIn(t, ['all', 'vars', 'pgbackrest_repo'], { local: { path: '/pg/backup' } })
    expect(check(t2).some((i) => i.level === 'error' && i.message.includes('pgbackrest_repo'))).toBe(true)
  })
})

describe('semanticDiff', () => {
  it('识别新增主机与修改变量', () => {
    let t = setIn(pigsty, ['all', 'children', 'pg-meta', 'hosts', '10.10.10.11'], { pg_seq: 2, pg_role: 'replica' })
    t = setIn(t, ['all', 'vars', 'pg_version'], 17)
    const changes = semanticDiff(parse(pigsty).toJS(), parse(t).toJS())
    expect(changes).toContainEqual(expect.objectContaining({ kind: 'add', scope: 'pg-meta / 10.10.10.11' }))
    expect(changes).toContainEqual(expect.objectContaining({ kind: 'change', scope: '全局', key: 'pg_version', before: 18, after: 17 }))
  })

  it('只改注释时没有语义变化', () => {
    const t = pigsty.replace('# pigsty version string', '# 版本')
    expect(semanticDiff(parse(pigsty).toJS(), parse(t).toJS())).toEqual([])
  })
})
