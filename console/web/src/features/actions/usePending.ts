import { useMemo } from 'react'
import { useQueries } from '@tanstack/react-query'
import { api } from '@/api/endpoints'
import { qk, useApplied, useSchema } from '@/api/queries'
import type { Applied } from '@/api/types'
import { buildModel, type ConfigModel } from '@/features/config/model'
import { useSavedModel } from '@/features/config/useSavedModel'
import { parse } from '@/features/config/yamlPatch'
import { planCluster, type ClusterPlan } from './planner'

/** 每个集群「最近应用版本 → 当前已保存配置」的待应用变更与建议动作 */
export function usePendingPlans() {
  const { config, model } = useSavedModel()
  const applied = useApplied()
  const schema = useSchema()
  const commits = useMemo(() => [...new Set((applied.data ?? []).map((a) => a.commit))], [applied.data])
  const texts = useQueries({
    queries: commits.map((hash) => ({
      queryKey: qk.historyItem(hash),
      queryFn: () => api.historyItem(hash),
      staleTime: Infinity,
    })),
  })

  const textsKey = texts.map((t) => t.dataUpdatedAt).join(',')
  const plans = useMemo(() => {
    const out = new Map<string, ClusterPlan & { applied?: Applied }>()
    if (!model) return out
    const models = new Map<string, ConfigModel>()
    commits.forEach((hash, i) => {
      const t = texts[i]?.data?.text
      if (t === undefined) return
      const doc = parse(t)
      if (!doc.errors.length) models.set(hash, buildModel(doc.toJS()))
    })
    const byCluster = new Map((applied.data ?? []).map((a) => [a.cluster, a]))
    for (const g of model.groups) {
      if (g.module !== 'PGSQL' && g.module !== 'REDIS') continue
      const a = byCluster.get(g.name)
      // 后端没有记录的集群（新增的）：视为全新
      const before = a ? models.get(a.commit) : undefined
      if (a && !before) continue // 历史版本还在加载
      out.set(g.name, { ...planCluster(g.name, before, model, schema.data), applied: a })
    }
    return out
    // texts 每次渲染都是新数组，用各查询的更新时间作为依赖
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [model, applied.data, schema.data, commits, textsKey])

  return { plans, loading: applied.isLoading || texts.some((t) => t.isLoading), configHash: config.data?.hash, error: applied.error }
}
