// 编译期核对：手写类型（types.ts）与后端 OpenAPI 生成的类型保持兼容。
// 后端接口变化后运行 `pnpm gen:api && pnpm typecheck`，不兼容会在这里报错。
import type { components } from './openapi'
import type * as T from './types'

type S = components['schemas']
type Assignable<From, To> = [From] extends [To] ? true : never
// 后端的数组字段在 OpenAPI 里是 `T[] | null`（endpoints.ts 已统一成空数组），枚举字段可能是 string
type NonNullArrays<X> = { [K in keyof X]: X[K] extends (infer E)[] | null ? NonNullArrays<E>[] : X[K] extends object ? NonNullArrays<X[K]> : X[K] }
type Strip<X> = NonNullArrays<Omit<X, '$schema'>>

export const contract: [
  Assignable<Strip<S['Commit']>, T.CommitInfo>,
  Assignable<Strip<S['ConfigBody']>, T.ConfigDoc>,
  Assignable<Strip<S['SaveBody']>, T.SaveResult>,
  Assignable<Strip<S['ValidateBody']>, T.ValidateResult>,
  Assignable<Strip<S['HistoryBody']>, T.HistoryPage>,
  Assignable<Strip<S['DiffBody']>, T.DiffResult>,
  Assignable<Strip<S['HostsBody']>, { hosts: T.InventoryHosts['hosts']; groups: { name: string; hosts: string[]; module: string | null }[] }>,
  Assignable<Strip<S['EffectiveBody']>, T.EffectiveVars>,
  Assignable<Strip<S['MetaBody']>, T.Meta>,
  Assignable<Strip<S['Issue']>, T.Issue>,
  // M2–M5
  Assignable<Strip<S['ActionDef']>, T.ActionDef>,
  Assignable<Strip<S['Plan']>, T.ActionPlan>,
  Assignable<Strip<S['Job']>, T.Job>,
  Assignable<Strip<S['JobsListOutputBody']>, T.JobPage>,
  Assignable<Strip<S['Applied']>, T.Applied>,
  Assignable<Strip<S['NodeStatus']>, T.NodeStatus>,
  Assignable<Strip<S['ClusterStatus']>, T.ClusterStatus>,
  Assignable<Strip<S['Drift']>, T.Drift>,
  Assignable<Strip<S['Alert']>, T.Alert>,
  Assignable<Strip<S['Settings']>, T.Settings>,
  Assignable<Strip<S['NodeSpec']>, T.NodeSpec>,
  Assignable<Strip<S['Info']>, T.BackupInfo>,
  Assignable<Strip<S['PitrPlan']>, T.PitrPlan>,
  Assignable<Strip<S['MetaBody']>, T.Meta>,
  // 请求体：前端发送的类型必须能赋给后端声明的输入
  Assignable<T.SubmitJob, Strip<S['JobCreateInputBody']>>,
  Assignable<T.PitrPlanRequest, Strip<S['PitrPlanInputBody']>>,
] = [true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true]
