import { describe, expect, it } from 'vitest'
import { buildModel } from '@/features/config/model'
import { planCluster } from './planner'

const base = {
  all: {
    vars: { pg_conf: 'oltp.yml' },
    children: {
      'pg-test': {
        hosts: { '10.0.0.1': { pg_seq: 1, pg_role: 'primary' }, '10.0.0.2': { pg_seq: 2, pg_role: 'replica' } },
        vars: {
          pg_cluster: 'pg-test',
          pg_users: [{ name: 'u1', password: 'x' }],
          pg_databases: [{ name: 'd1' }],
          pg_hba_rules: [],
          pg_parameters: { max_connections: 500 },
        },
      },
    },
  },
}

const clone = () => JSON.parse(JSON.stringify(base))

describe('planCluster', () => {
  it('无变化', () => {
    const p = planCluster('pg-test', buildModel(base), buildModel(base))
    expect(p.changed).toBe(false)
    expect(p.suggestions).toEqual([])
  })

  it('新增用户 / 修改库 / HBA / 参数', () => {
    const after = clone()
    const v = after.all.children['pg-test'].vars
    v.pg_users.push({ name: 'u2', password: 'y' })
    v.pg_databases[0].owner = 'u2'
    v.pg_hba_rules = [{ user: 'u2', db: 'd1', addr: 'intra', auth: 'pwd' }]
    v.pg_parameters = { max_connections: 600, work_mem: '64MB' }
    const p = planCluster('pg-test', buildModel(base), buildModel(after))
    expect(p.suggestions.map((s) => [s.action, s.params])).toEqual([
      ['pgsql-user', { cluster: 'pg-test', user: 'u2' }],
      ['pgsql-db', { cluster: 'pg-test', db: 'd1' }],
      ['pgsql-hba', { cluster: 'pg-test' }],
      ['patroni-param', { cluster: 'pg-test', settings: { max_connections: '600', work_mem: '64MB' } }],
    ])
  })

  it('新增主机：先纳管再加从库；删除主机给出警告', () => {
    const after = clone()
    const h = after.all.children['pg-test'].hosts
    h['10.0.0.3'] = { pg_seq: 3, pg_role: 'replica' }
    delete h['10.0.0.2']
    const p = planCluster('pg-test', buildModel(base), buildModel(after))
    expect(p.suggestions.map((s) => s.action)).toEqual(['node-add', 'pgsql-add-replica'])
    expect(p.notes.some((n) => n.level === 'warning' && n.message.includes('10.0.0.2'))).toBe(true)
  })

  it('初始化参数变化只警告', () => {
    const after = clone()
    after.all.children['pg-test'].vars.pg_shared_buffer_ratio = 0.4
    const p = planCluster('pg-test', buildModel(base), buildModel(after))
    expect(p.suggestions).toEqual([])
    expect(p.notes[0].message).toContain('只在集群初始化时生效')
  })

  it('新的 Redis 集群', () => {
    const after = clone()
    after.all.children['redis-a'] = { hosts: { '10.0.0.9': { redis_node: 1, redis_instances: { 6379: {} } } }, vars: { redis_cluster: 'redis-a' } }
    const p = planCluster('redis-a', buildModel(base), buildModel(after))
    expect(p.suggestions.map((s) => [s.action, s.params])).toEqual([
      ['node-add', { ips: ['10.0.0.9'] }],
      ['redis-create', { cluster: 'redis-a' }],
    ])
  })
})
