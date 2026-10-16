import { describe, expect, it } from 'vitest'
import { buildModel } from '@/features/config/model'
import { estimateNode, parseSizeMb, pgParams } from './estimate'

describe('pgParams（与模板摘要行一致）', () => {
  it('oltp 16G，auto 连接数，默认服务指向 pgbouncer', () => {
    expect(pgParams(16384, 'oltp', 'auto', 0.25, 'pgbouncer')).toMatchObject({
      maxConnections: 500,
      sharedBuffers: 4096,
      maintenanceWorkMem: 1024,
      workMem: 64,
      effectiveCacheSize: 12288,
    })
  })
  it('oltp 指向 postgres 时 1000 连接', () => {
    expect(pgParams(8192, 'oltp', 'auto', 0.25, 'postgres').maxConnections).toBe(1000)
  })
  it('olap 64G 显式 max_conn', () => {
    const p = pgParams(65536, 'olap', 100, 0.4, 'pgbouncer')
    expect(p).toMatchObject({ maxConnections: 100, sharedBuffers: 26215, maintenanceWorkMem: 13108, workMem: 262 })
  })
  it('max_conn 小于 20 视为 auto', () => {
    expect(pgParams(4096, 'tiny', 10, 0.25, 'pgbouncer').maxConnections).toBe(250)
  })
  it('tiny work_mem 下限 16', () => {
    expect(pgParams(1024, 'tiny', 'auto', 0.25, 'pgbouncer').workMem).toBe(16)
  })
  it('work_mem 上限', () => {
    expect(pgParams(1024 * 1024, 'oltp', 100, 0.25, 'pgbouncer').workMem).toBe(1024)
  })
})

describe('parseSizeMb', () => {
  it.each([
    ['1GB', 1024],
    ['64MB', 64],
    ['512kb', 0.5],
    ['2g', 2048],
  ])('%s', (s, n) => expect(parseSizeMb(s)).toBe(n))
})

describe('estimateNode', () => {
  const model = buildModel({
    all: {
      vars: { pg_conf: 'oltp.yml' },
      children: {
        infra: { hosts: { '10.0.0.1': { infra_seq: 1 } } },
        'pg-test': {
          hosts: { '10.0.0.1': { pg_seq: 1, pg_role: 'primary' }, '10.0.0.2': { pg_seq: 2, pg_role: 'replica' } },
          vars: { pg_cluster: 'pg-test' },
        },
        'redis-ms': {
          hosts: { '10.0.0.2': { redis_node: 1, redis_instances: { 6379: {}, 6380: {} } } },
          vars: { redis_cluster: 'redis-ms', redis_max_memory: '1GB' },
        },
      },
    },
  })
  const specs = { '10.0.0.1': { memMb: 16384, cpu: 8, source: 'spec' as const }, '10.0.0.2': { memMb: 8192, cpu: 4, source: 'spec' as const } }

  it('replica 按主库内存计算 shared_buffers 并给出警告', () => {
    const e = estimateNode('10.0.0.2', { model, specs })
    expect(e.pg?.sharedBuffers).toBe(4096)
    expect(e.pg?.basedOn).toBe('10.0.0.1')
    expect(e.warnings.some((w) => w.includes('OOM'))).toBe(true)
    expect(e.components.find((c) => c.id === 'redis:redis-ms')?.typical).toBeCloseTo(2 * (1024 * 1.3 + 10))
  })
  it('单机全家桶包含 INFRA 组件', () => {
    const e = estimateNode('10.0.0.1', { model, specs })
    expect(e.modules).toEqual(expect.arrayContaining(['INFRA', 'PGSQL']))
    expect(e.components.some((c) => c.id === 'victoria_metrics')).toBe(true)
    expect(e.level).not.toBe('unknown')
  })
  it('规格未知', () => {
    const e = estimateNode('10.0.0.2', { model, specs: {} })
    expect(e.level).toBe('unknown')
  })
})
