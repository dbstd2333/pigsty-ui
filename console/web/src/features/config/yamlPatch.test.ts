import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { appendItem, deleteIn, deleteItem, getIn, parse, setIn, setItem } from './yamlPatch'

const ROOT = join(import.meta.dirname, '../../../../..')
const pigsty = readFileSync(join(ROOT, 'pigsty.yml'), 'utf8')

function changedLines(a: string, b: string) {
  const x = a.split('\n')
  const y = b.split('\n')
  let i = 0
  while (i < x.length && x[i] === y[i]) i++
  let j = 0
  while (j < x.length - i && x[x.length - 1 - j] === y[y.length - 1 - j]) j++
  return { removed: x.slice(i, x.length - j), added: y.slice(i, y.length - j) }
}

function js(text: string) {
  return parse(text).toJS()
}

describe('setIn 标量', () => {
  it('只改动一行且保留行尾注释', () => {
    const out = setIn(pigsty, ['all', 'vars', 'pg_version'], 17)
    const d = changedLines(pigsty, out)
    expect(d.removed).toHaveLength(1)
    expect(d.added).toEqual(['    pg_version: 17                      # default postgres version'])
    expect(js(out).all.vars.pg_version).toBe(17)
  })

  it('flow 映射中的值只改动该值', () => {
    const out = setIn(pigsty, ['all', 'children', 'pg-meta', 'hosts', '10.10.10.10', 'pg_role'], 'replica')
    const d = changedLines(pigsty, out)
    expect(d.removed).toHaveLength(1)
    expect(d.added[0]).toContain('{ pg_seq: 1, pg_role: replica }')
    expect(d.added[0]).toContain('# <---- primary instance')
  })

  it('需要引号的字符串会被正确引用', () => {
    const out = setIn(pigsty, ['all', 'vars', 'region'], 'a: b')
    expect(js(out).all.vars.region).toBe('a: b')
  })
})

describe('setIn 新增键', () => {
  it('块映射末尾追加一行', () => {
    const out = setIn(pigsty, ['all', 'vars', 'pg_max_conn'], 500)
    const d = changedLines(pigsty, out)
    expect(d.removed).toHaveLength(0)
    expect(d.added).toEqual(['    pg_max_conn: 500'])
    expect(js(out).all.vars.pg_max_conn).toBe(500)
  })

  it('flow 映射中追加', () => {
    const out = setIn(pigsty, ['all', 'children', 'pg-meta', 'hosts', '10.10.10.10', 'pg_offline_query'], true)
    expect(js(out).all.children['pg-meta'].hosts['10.10.10.10']).toEqual({
      pg_seq: 1,
      pg_role: 'primary',
      pg_offline_query: true,
    })
    expect(changedLines(pigsty, out).removed).toHaveLength(1)
  })

  it('父级不存在时逐级创建', () => {
    const out = setIn(pigsty, ['all', 'children', 'etcd', 'vars', 'etcd_port', 'x'], 1)
    expect(js(out).all.children.etcd.vars.etcd_port).toEqual({ x: 1 })
  })

  it('新增对象数组按 Pigsty 风格输出', () => {
    const out = setIn(pigsty, ['all', 'children', 'infra', 'vars', 'demo_users'], [{ name: 'a', roles: ['r'] }])
    expect(out).toContain('        demo_users:\n          - { name: a, roles: [ r ] }\n')
    expect(js(out).all.children.infra.vars.demo_users).toEqual([{ name: 'a', roles: ['r'] }])
  })
})

describe('setIn 替换集合', () => {
  it('块序列替换后保留键所在行的注释', () => {
    const rules = [{ user: 'all', db: 'all', addr: 'world', auth: 'pwd', title: 't', order: 900 }]
    const out = setIn(pigsty, ['all', 'children', 'pg-meta', 'vars', 'pg_hba_rules'], rules)
    expect(out).toContain('pg_hba_rules:   # https://pigsty.io/docs/pgsql/config/hba')
    expect(js(out).all.children['pg-meta'].vars.pg_hba_rules).toEqual(rules)
    expect(js(out).all.children['pg-meta'].vars.pg_crontab).toEqual(['00 01 * * * /pg/bin/pg-backup full'])
  })

  it('flow 序列替换为内联', () => {
    const out = setIn(pigsty, ['all', 'children', 'pg-meta', 'vars', 'pg_extensions'], ['postgis'])
    const d = changedLines(pigsty, out)
    expect(d.removed).toHaveLength(1)
    expect(js(out).all.children['pg-meta'].vars.pg_extensions).toEqual(['postgis'])
  })
})

describe('deleteIn', () => {
  it('删除块映射中的一行', () => {
    const out = deleteIn(pigsty, ['all', 'vars', 'node_tune'])
    const d = changedLines(pigsty, out)
    expect(d.added).toHaveLength(0)
    expect(d.removed).toHaveLength(1)
    expect(js(out).all.vars.node_tune).toBeUndefined()
  })

  it('删除带块值的键', () => {
    const out = deleteIn(pigsty, ['all', 'vars', 'proxy_env'])
    const v = js(out).all.vars
    expect(v.proxy_env).toBeUndefined()
    expect(v.infra_portal).toBeDefined()
  })

  it('删除 flow 映射中的键', () => {
    const out = deleteIn(pigsty, ['all', 'children', 'infra', 'hosts', '10.10.10.10', 'infra_seq'])
    expect(js(out).all.children.infra.hosts['10.10.10.10']).toEqual({})
  })

  it('删空块映射时连同父键删除', () => {
    let out = deleteIn(pigsty, ['all', 'children', 'etcd', 'vars', 'etcd_cluster'])
    out = deleteIn(out, ['all', 'children', 'etcd', 'vars', 'etcd_safeguard'])
    expect(js(out).all.children.etcd.vars).toBeUndefined()
    expect(js(out).all.children.etcd.hosts).toBeDefined()
  })
})

const USERS = ['all', 'children', 'pg-meta', 'vars', 'pg_users']
const DBS = ['all', 'children', 'pg-meta', 'vars', 'pg_databases']

describe('列表项操作', () => {
  it('替换 flow 项只改一行', () => {
    const u = { name: 'dbuser_view', password: 'x', roles: ['dbrole_readonly'] }
    const out = setItem(pigsty, USERS, 1, u)
    const d = changedLines(pigsty, out)
    expect(d.removed).toHaveLength(1)
    expect(d.added[0]).toMatch(/^ {10}- \{ name: dbuser_view/)
    expect(js(out).all.children['pg-meta'].vars.pg_users[1]).toEqual(u)
    expect(js(out).all.children['pg-meta'].vars.pg_users[0].name).toBe('dbuser_meta')
  })

  it('替换块映射项保持块写法与缩进', () => {
    const db = { name: 'meta', baseline: 'cmdb.sql', schemas: ['pigsty'], extensions: ['vector'] }
    const out = setItem(pigsty, DBS, 0, db)
    expect(out).toContain('          - name: meta\n            baseline: cmdb.sql\n            schemas: [ pigsty ]\n            extensions: [ vector ]\n')
    expect(js(out).all.children['pg-meta'].vars.pg_databases).toEqual([db])
    expect(js(out).all.children['pg-meta'].vars.pg_hba_rules).toHaveLength(1)
  })

  it('追加到块序列末尾', () => {
    const out = appendItem(pigsty, USERS, { name: 'u3' })
    const d = changedLines(pigsty, out)
    expect(d.removed).toHaveLength(0)
    expect(d.added).toEqual(['          - { name: u3 }'])
  })

  it('列表不存在时创建', () => {
    const out = appendItem(pigsty, ['all', 'children', 'pg-meta', 'vars', 'pg_services'], { name: 's', port: 5440 })
    expect(js(out).all.children['pg-meta'].vars.pg_services).toEqual([{ name: 's', port: 5440 }])
  })

  it('删除块序列中的项', () => {
    const out = deleteItem(pigsty, USERS, 0)
    expect(js(out).all.children['pg-meta'].vars.pg_users.map((u: { name: string }) => u.name)).toEqual(['dbuser_view'])
    expect(changedLines(pigsty, out).removed).toHaveLength(1)
  })

  it('删除多行块映射项', () => {
    const out = deleteItem(pigsty, DBS, 0)
    expect(js(out).all.children['pg-meta'].vars.pg_databases).toEqual([])
    expect(js(out).all.children['pg-meta'].vars.pg_hba_rules).toHaveLength(1)
  })

  it('flow 序列中操作', () => {
    const p = ['all', 'children', 'pg-meta', 'vars', 'pg_extensions']
    expect(js(appendItem(pigsty, p, 'timescaledb')).all.children['pg-meta'].vars.pg_extensions).toEqual(['postgis', 'pgvector', 'timescaledb'])
    expect(js(deleteItem(pigsty, p, 0)).all.children['pg-meta'].vars.pg_extensions).toEqual(['pgvector'])
  })
})

describe('conf/ 全部模板', () => {
  const files: string[] = []
  const walk = (dir: string) => {
    for (const f of readdirSync(dir)) {
      const p = join(dir, f)
      if (statSync(p).isDirectory()) walk(p)
      else if (f.endsWith('.yml')) files.push(p)
    }
  }
  walk(join(ROOT, 'conf'))

  it.each(files.map((f) => [f.slice(ROOT.length + 1), f]))('%s：改一个标量只影响一行', (_, file) => {
    const text = readFileSync(file, 'utf8')
    const doc = parse(text)
    if (getIn(doc, ['all', 'vars', 'admin_ip']) === undefined) return
    const out = setIn(text, ['all', 'vars', 'admin_ip'], '10.10.10.99')
    const d = changedLines(text, out)
    expect(d.removed).toHaveLength(1)
    expect(d.added).toHaveLength(1)
    expect(parse(out).toJS().all.vars.admin_ip).toBe('10.10.10.99')
  })
})
