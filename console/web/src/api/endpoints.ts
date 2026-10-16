import { request } from './http'
import type {
  ActionDef,
  ActionPlan,
  Alert,
  Applied,
  BackupInfo,
  ClusterStatus,
  Drift,
  Job,
  JobPage,
  NodeSpec,
  NodeStatus,
  PitrPlan,
  PitrPlanRequest,
  Settings,
  SourceInfo,
  SubmitJob,
  ConfigDoc,
  DiffResult,
  EffectiveVars,
  HistoryPage,
  InventoryHosts,
  Meta,
  ParamSchema,
  SaveResult,
  CommitInfo,
  ValidateResult,
} from './types'

export const api = {
  meta: () => request<Meta>('GET', '/meta'),

  me: () => request<{ username: string }>('GET', '/auth/me'),
  login: (username: string, password: string) =>
    request<{ username: string }>('POST', '/auth/login', { username, password }),
  logout: () => request<void>('POST', '/auth/logout'),
  changePassword: (oldPassword: string, newPassword: string) =>
    request<void>('PUT', '/auth/password', { oldPassword, newPassword }),

  getConfig: () => request<ConfigDoc>('GET', '/config'),
  saveConfig: (text: string, baseHash: string, message: string) =>
    request<SaveResult>('PUT', '/config', { text, baseHash, message }),
  // Go 的空切片会序列化成 null，这里统一成空数组
  validateConfig: (text: string) =>
    request<ValidateResult>('POST', '/config/validate', { text }).then((r) => ({ ...r, issues: r.issues ?? [] })),
  history: (limit = 50, offset = 0) =>
    request<HistoryPage>('GET', `/config/history?limit=${limit}&offset=${offset}`).then((r) => ({ ...r, items: r.items ?? [] })),
  historyItem: (hash: string) =>
    request<{ commit: CommitInfo; text: string }>('GET', `/config/history/${encodeURIComponent(hash)}`),
  diff: (from: string, to = 'HEAD') =>
    request<DiffResult>('GET', `/config/diff?from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}`),
  revert: (hash: string, baseHash: string, message?: string) =>
    request<SaveResult>('POST', '/config/revert', { hash, baseHash, message }),

  inventoryHosts: () =>
    request<InventoryHosts>('GET', '/inventory/hosts').then((r) => ({
      hosts: (r.hosts ?? []).map((h) => ({ ...h, groups: h.groups ?? [] })),
      groups: (r.groups ?? []).map((g) => ({ ...g, hosts: g.hosts ?? [] })),
    })),
  effective: (host: string) =>
    request<EffectiveVars>('GET', `/inventory/effective?host=${encodeURIComponent(host)}`).then((r) => ({ ...r, vars: r.vars ?? [] })),

  schema: () => request<ParamSchema>('GET', '/schema'),

  // ---------- M2–M5 ----------
  actions: () => request<{ items: ActionDef[] }>('GET', '/actions').then((r) => r.items ?? []),
  planAction: (id: string, params: Record<string, unknown>, check: boolean) =>
    request<ActionPlan>('POST', `/actions/${encodeURIComponent(id)}/plan`, { params, check }),

  submitJob: (body: SubmitJob) => request<Job>('POST', '/jobs', body),
  jobs: (q: { limit?: number; offset?: number; cluster?: string; status?: string } = {}) =>
    request<JobPage>('GET', `/jobs${qs(q)}`).then((r) => ({ ...r, items: r.items ?? [] })),
  job: (id: string) => request<Job>('GET', `/jobs/${encodeURIComponent(id)}`),
  jobLog: (id: string) => fetch(`/api/v1/jobs/${encodeURIComponent(id)}/log`, { credentials: 'same-origin' }).then((r) => r.text()),
  cancelJob: (id: string) => request<void>('POST', `/jobs/${encodeURIComponent(id)}/cancel`),

  applied: () => request<{ items: Applied[] }>('GET', '/applied').then((r) => r.items ?? []),

  statusNodes: () => request<{ source: SourceInfo; items: NodeStatus[] }>('GET', '/status/nodes').then((r) => ({ ...r, items: r.items ?? [] })),
  statusClusters: () => request<{ items: ClusterStatus[] }>('GET', '/status/clusters').then((r) => (r.items ?? []).map(normCluster)),
  statusCluster: (cls: string) => request<ClusterStatus>('GET', `/status/clusters/${encodeURIComponent(cls)}`).then(normCluster),
  pgConfig: (cls: string) =>
    request<{ source: SourceInfo; parameters: Record<string, string> }>('GET', `/status/clusters/${encodeURIComponent(cls)}/pgconfig`),
  drift: () => request<{ items: Drift[] }>('GET', '/status/drift').then((r) => r.items ?? []),
  alerts: () => request<{ source: SourceInfo; items: Alert[] }>('GET', '/status/alerts').then((r) => ({ ...r, items: r.items ?? [] })),
  nodeMemory: (ip: string, refresh = false) =>
    request<{ source: SourceInfo; services: { name: string; bytes: number | null }[] }>(
      'GET',
      `/status/nodes/${encodeURIComponent(ip)}/memory${refresh ? '?refresh=1' : ''}`,
    ).then((r) => ({ ...r, services: r.services ?? [] })),

  settings: () => request<Settings>('GET', '/settings'),
  saveSettings: (s: Partial<Omit<Settings, 'derived'>>) => request<Settings>('PUT', '/settings', s),

  nodeSpecs: () => request<{ items: NodeSpec[] }>('GET', '/node-specs').then((r) => r.items ?? []),
  saveNodeSpec: (ip: string, spec: { cpu: number; memMb: number; diskGb: number }) =>
    request<NodeSpec>('PUT', `/node-specs/${encodeURIComponent(ip)}`, spec),
  deleteNodeSpec: (ip: string) => request<void>('DELETE', `/node-specs/${encodeURIComponent(ip)}`),

  backups: (cls: string, refresh = false) =>
    request<BackupInfo>('GET', `/backups/${encodeURIComponent(cls)}${refresh ? '?refresh=1' : ''}`).then((r) => ({
      ...r,
      backups: r.backups ?? [],
      restorePoints: r.restorePoints ?? [],
      events: r.events ?? [],
    })),
  pitrPlan: (cls: string, body: PitrPlanRequest) =>
    request<PitrPlan>('POST', `/backups/${encodeURIComponent(cls)}/pitr/plan`, body).then((r) => ({
      ...r,
      commands: r.commands ?? [],
      preconditions: r.preconditions ?? [],
    })),
}

function qs(q: Record<string, string | number | undefined>) {
  const p = Object.entries(q).filter(([, v]) => v !== undefined && v !== '')
  return p.length ? '?' + p.map(([k, v]) => `${k}=${encodeURIComponent(String(v))}`).join('&') : ''
}

function normCluster(c: ClusterStatus): ClusterStatus {
  return { ...c, members: c.members ?? [] }
}
