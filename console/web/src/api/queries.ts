import { useQuery } from '@tanstack/react-query'
import { api } from './endpoints'

export const qk = {
  me: ['me'] as const,
  meta: ['meta'] as const,
  config: ['config'] as const,
  history: (limit: number, offset: number) => ['config', 'history', limit, offset] as const,
  historyItem: (hash: string) => ['config', 'history', hash] as const,
  diff: (from: string, to: string) => ['config', 'diff', from, to] as const,
  hosts: (hash: string) => ['inventory', 'hosts', hash] as const,
  effective: (host: string, hash: string) => ['inventory', 'effective', host, hash] as const,
  schema: ['schema'] as const,
  actions: ['actions'] as const,
  jobs: (q: object) => ['jobs', q] as const,
  job: (id: string) => ['jobs', 'one', id] as const,
  applied: ['applied'] as const,
  statusNodes: ['status', 'nodes'] as const,
  statusClusters: ['status', 'clusters'] as const,
  statusCluster: (cls: string) => ['status', 'clusters', cls] as const,
  pgConfig: (cls: string) => ['status', 'pgconfig', cls] as const,
  drift: ['status', 'drift'] as const,
  alerts: ['status', 'alerts'] as const,
  nodeMemory: (ip: string) => ['status', 'memory', ip] as const,
  settings: ['settings'] as const,
  nodeSpecs: ['node-specs'] as const,
  backups: (cls: string) => ['backups', cls] as const,
}

export const useMeta = () => useQuery({ queryKey: qk.meta, queryFn: api.meta, staleTime: 60_000 })

// 30 秒轮询，用于发现 UI 之外的手工修改
export const useConfig = () =>
  useQuery({ queryKey: qk.config, queryFn: api.getConfig, refetchInterval: 30_000 })

export const useSchema = () =>
  useQuery({ queryKey: qk.schema, queryFn: api.schema, staleTime: Infinity })

export const useHistory = (limit: number, offset: number) =>
  useQuery({ queryKey: qk.history(limit, offset), queryFn: () => api.history(limit, offset) })

// 有效值依赖已保存的配置，按 hash 缓存
export const useInventoryHosts = (hash: string | undefined) =>
  useQuery({ queryKey: qk.hosts(hash ?? ''), queryFn: api.inventoryHosts, enabled: !!hash })

export const useEffective = (host: string | undefined, hash: string | undefined) =>
  useQuery({
    queryKey: qk.effective(host ?? '', hash ?? ''),
    queryFn: () => api.effective(host!),
    enabled: !!host && !!hash,
  })

export const useActions = () => useQuery({ queryKey: qk.actions, queryFn: api.actions, staleTime: Infinity })

export const useJobs = (q: { limit?: number; offset?: number; cluster?: string; status?: string } = {}) =>
  useQuery({
    queryKey: qk.jobs(q),
    queryFn: () => api.jobs(q),
    // 有作业在跑时自动刷新
    refetchInterval: (query) => (query.state.data?.items.some((j) => j.status === 'running' || j.status === 'queued') ? 3_000 : 30_000),
  })

export const useApplied = () => useQuery({ queryKey: qk.applied, queryFn: api.applied })

export const useStatusNodes = () =>
  useQuery({ queryKey: qk.statusNodes, queryFn: api.statusNodes, refetchInterval: 30_000 })

export const useStatusClusters = () =>
  useQuery({ queryKey: qk.statusClusters, queryFn: api.statusClusters, refetchInterval: 30_000 })

export const useStatusCluster = (cls: string) =>
  useQuery({ queryKey: qk.statusCluster(cls), queryFn: () => api.statusCluster(cls), refetchInterval: 15_000 })

export const usePgConfig = (cls: string, enabled = true) =>
  useQuery({ queryKey: qk.pgConfig(cls), queryFn: () => api.pgConfig(cls), enabled })

export const useDrift = () => useQuery({ queryKey: qk.drift, queryFn: api.drift, refetchInterval: 60_000 })

export const useAlerts = () => useQuery({ queryKey: qk.alerts, queryFn: api.alerts, refetchInterval: 30_000 })

export const useSettings = () => useQuery({ queryKey: qk.settings, queryFn: api.settings })

export const useNodeSpecs = () => useQuery({ queryKey: qk.nodeSpecs, queryFn: api.nodeSpecs })

export const useBackups = (cls: string) => useQuery({ queryKey: qk.backups(cls), queryFn: () => api.backups(cls) })
