import { useMemo } from 'react'
import { Link } from '@tanstack/react-router'
import { Empty, Space, Typography } from 'antd'
import type { Issue, ParamSchema } from '@/api/types'
import type { ConfigModel, Scope } from '@/features/config/model'
import { compareIp } from '@/features/config/model'
import { estimateAll, type NodeEstimate, type NodeSpecInput } from './estimate'
import { fmtMb, LevelTag, MemoryBar } from './MemoryBar'

/** 工作台右侧：当前作用域涉及节点的内存预估，与已保存版本对比 */
export function MemoryPanel({
  estimates,
  saved,
  scope,
  model,
}: {
  estimates: NodeEstimate[]
  saved: NodeEstimate[]
  scope: Scope
  model: ConfigModel
}) {
  const ips = useMemo(() => {
    if (scope.kind === 'host') return [scope.host]
    if (scope.kind === 'group') return Object.keys(model.groups.find((g) => g.name === scope.group)?.hosts ?? {}).sort(compareIp)
    // 全局：先显示有风险的节点
    return estimates.filter((e) => e.level === 'danger' || e.level === 'warn').map((e) => e.ip)
  }, [scope, model, estimates])
  const before = new Map(saved.map((e) => [e.ip, e]))
  const rows = ips.map((ip) => estimates.find((e) => e.ip === ip)).filter(Boolean) as NodeEstimate[]

  if (!rows.length)
    return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={scope.kind === 'global' ? '没有内存偏高的节点。选择组或主机查看明细。' : '没有节点'} />

  return (
    <div style={{ display: 'grid', gap: 14 }}>
      {rows.map((e) => {
        const b = before.get(e.ip)
        const delta = b ? e.typical - b.typical : 0
        return (
          <div key={e.ip}>
            <Space size={4} style={{ marginBottom: 2 }} wrap>
              <span className="mono" style={{ fontSize: 12 }}>
                {e.ip}
              </span>
              <LevelTag level={e.level} />
              {Math.abs(delta) >= 1 && (
                <Typography.Text type={delta > 0 ? 'warning' : 'success'} style={{ fontSize: 12 }}>
                  {delta > 0 ? '+' : '−'}
                  {fmtMb(Math.abs(delta))}
                </Typography.Text>
              )}
            </Space>
            <MemoryBar e={e} />
            {e.warnings.map((w) => (
              <div key={w} style={{ fontSize: 12, color: '#d48806' }}>
                {w}
              </div>
            ))}
          </div>
        )
      })}
      <Link to="/capacity" style={{ fontSize: 12 }}>
        查看容量页的公式明细 →
      </Link>
    </div>
  )
}

/** 保存前的内存警告（只警告，不阻止保存） */
export function memoryIssues(estimates: NodeEstimate[]): Issue[] {
  return estimates
    .filter((e) => e.level === 'danger')
    .map((e) => ({
      level: 'warning' as const,
      source: 'semantic' as const,
      message: `节点 ${e.ip} 内存可能超配：典型占用 ${fmtMb(e.typical)} / ${fmtMb(e.spec!.memMb)}`,
      path: `all.children.${e.groups[0] ?? ''}.hosts.${e.ip}`,
    }))
}

export function useEstimates(model: ConfigModel | undefined, schema: ParamSchema | undefined, specs: Record<string, NodeSpecInput>, baseline?: Record<string, number>) {
  return useMemo(() => (model ? estimateAll({ model, schema, specs, baseline }) : []), [model, schema, specs, baseline])
}
