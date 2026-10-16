import { Progress, Space, Tag, Tooltip, Typography } from 'antd'
import type { Layer, Level, NodeEstimate } from './estimate'

export const LAYER_META: Record<Layer, { label: string; color: string }> = {
  pg: { label: 'PG', color: '#1677ff' },
  'pg-side': { label: 'PG 周边', color: '#69b1ff' },
  node: { label: 'NODE', color: '#8c8c8c' },
  etcd: { label: 'ETCD', color: '#d4b106' },
  minio: { label: 'MINIO', color: '#13c2c2' },
  infra: { label: 'INFRA', color: '#722ed1' },
  redis: { label: 'REDIS', color: '#f5222d' },
  app: { label: 'APP', color: '#52c41a' },
  system: { label: '系统预留', color: '#bfbfbf' },
}

export const LEVEL_META: Record<Level, { color: string; label: string }> = {
  ok: { color: 'success', label: '正常' },
  warn: { color: 'warning', label: '偏高' },
  danger: { color: 'error', label: '超配风险' },
  unknown: { color: 'default', label: '规格未知' },
}

export function LevelTag({ level }: { level: Level }) {
  const m = LEVEL_META[level]
  return <Tag color={m.color}>{m.label}</Tag>
}

export const fmtMb = (n: number) => (n >= 10240 ? `${(n / 1024).toFixed(1)} GB` : `${Math.round(n)} MB`)

/** 按层堆叠的内存条（典型值），总长 = 节点内存 */
export function MemoryBar({ e, width = '100%' }: { e: NodeEstimate; width?: number | string }) {
  if (!e.spec) return <Typography.Text type="secondary">规格未知</Typography.Text>
  const total = e.spec.memMb
  const byLayer = new Map<Layer, number>()
  for (const c of e.components) byLayer.set(c.layer, (byLayer.get(c.layer) ?? 0) + c.typical)
  const pct = (e.typical / total) * 100
  return (
    <div style={{ width }}>
      <Tooltip
        title={
          <div>
            {[...byLayer.entries()].map(([l, v]) => (
              <div key={l}>
                {LAYER_META[l].label}：{fmtMb(v)}
              </div>
            ))}
            <div>典型合计：{fmtMb(e.typical)} / {fmtMb(total)}</div>
          </div>
        }
      >
        <div style={{ display: 'flex', height: 12, borderRadius: 3, overflow: 'hidden', background: '#f0f0f0', outline: pct > 100 ? '2px solid #ff4d4f' : undefined }}>
          {[...byLayer.entries()].map(([l, v]) => (
            <div key={l} style={{ width: `${Math.min(100, (v / total) * 100)}%`, background: LAYER_META[l].color }} />
          ))}
        </div>
      </Tooltip>
      <Space size={6} style={{ fontSize: 12, marginTop: 2 }}>
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          常驻 {fmtMb(e.resident)} · 典型 {fmtMb(e.typical)}（{pct.toFixed(0)}%）· 上界 {fmtMb(e.peak)}
        </Typography.Text>
      </Space>
    </div>
  )
}

export function UsageProgress({ used, total }: { used: number | null; total: number | null }) {
  if (used === null || !total) return <Typography.Text type="secondary">-</Typography.Text>
  const p = Math.round((used / total) * 100)
  return <Progress percent={p} size="small" status={p > 90 ? 'exception' : 'normal'} strokeColor={p > 75 && p <= 90 ? '#faad14' : undefined} />
}
