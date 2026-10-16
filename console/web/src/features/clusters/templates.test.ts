import { describe, expect, it } from 'vitest'
import { buildModel } from '@/features/config/model'
import { parse, setIn } from '@/features/config/yamlPatch'
import { pgGroup, redisGroup, validatePg, validateRedis, type PgSpec, type RedisSpec } from './templates'

const pg: PgSpec = {
  name: 'pg-new',
  version: '',
  conf: '',
  instances: [
    { ip: '10.0.0.1', role: 'primary' },
    { ip: '10.0.0.2', role: 'offline' },
  ],
  users: [{ name: 'dbuser_app', password: 'p', role: 'dbrole_readwrite', pgbouncer: true, comment: '' }],
  databases: [{ name: 'app', owner: 'dbuser_app', comment: '' }],
  vip: '',
  vipInterface: 'eth0',
}

const redis: RedisSpec = {
  name: 'redis-new',
  mode: 'standalone',
  maxMemory: '1GB',
  password: 'r',
  nodes: [{ ip: '10.0.0.5', ports: [6379, 6380], replicaOf: { 6380: '10.0.0.5 6379' } }],
  clusterReplicas: 1,
  monitors: [],
}

const doc = 'all:\n  children:\n    infra: { hosts: { 10.0.0.1: { infra_seq: 1 } } }\n  vars:\n    version: v4.5.0\n'

describe('新建集群片段', () => {
  it('PG 片段写入后可被识别为 PGSQL 组', () => {
    expect(validatePg(pg, ['infra'])).toEqual([])
    const out = setIn(doc, ['all', 'children', 'pg-new'], pgGroup(pg))
    const m = buildModel(parse(out).toJS())
    const g = m.groups.find((x) => x.name === 'pg-new')!
    expect(g.module).toBe('PGSQL')
    expect(g.hosts['10.0.0.2']).toEqual({ pg_seq: 2, pg_role: 'replica', pg_offline_query: true })
    expect(out.startsWith(doc.split('  vars:')[0])).toBe(true)
  })

  it('Redis 主从片段：端口为整数键，replica_of 正确', () => {
    expect(validateRedis(redis, [])).toEqual([])
    const out = setIn(doc, ['all', 'children', 'redis-new'], redisGroup(redis))
    const js = parse(out).toJS() as { all: { children: Record<string, { hosts: Record<string, { redis_instances: Record<string, unknown> }> }> } }
    expect(js.all.children['redis-new'].hosts['10.0.0.5'].redis_instances).toEqual({ 6379: {}, 6380: { replica_of: '10.0.0.5 6379' } })
    expect(buildModel(js).groups.find((x) => x.name === 'redis-new')!.module).toBe('REDIS')
  })

  it('校验', () => {
    expect(validatePg({ ...pg, instances: [{ ip: '10.0.0.1', role: 'replica' }] }, [])).toContain('必须且只能有一个主库（primary）')
    expect(validateRedis({ ...redis, mode: 'cluster', nodes: [{ ip: '10.0.0.5', ports: [6379], replicaOf: {} }] }, []).join()).toContain('至少需要 6 个实例')
  })
})
