import { useMemo } from 'react'
import { useNodeSpecs, useSettings, useStatusNodes } from '@/api/queries'
import type { NodeSpecInput } from './estimate'

/** 节点规格：优先监控指标，其次规划规格（node-specs） */
export function useSpecsMap() {
  const nodes = useStatusNodes()
  const specs = useNodeSpecs()
  const settings = useSettings()
  const map = useMemo(() => {
    const out: Record<string, NodeSpecInput> = {}
    for (const s of specs.data ?? []) out[s.ip] = { memMb: s.memMb, cpu: s.cpu, source: 'spec' }
    for (const n of nodes.data?.items ?? [])
      if (n.memTotalBytes) out[n.ip] = { memMb: Math.round(n.memTotalBytes / 1024 / 1024), cpu: n.cpuCores ?? out[n.ip]?.cpu ?? 1, source: 'metrics' }
    return out
  }, [nodes.data, specs.data])
  return { specs: map, baseline: settings.data?.memoryBaseline, nodes, nodeSpecs: specs }
}
