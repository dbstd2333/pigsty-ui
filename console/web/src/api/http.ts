import type { Issue } from './types'

// RFC 9457 problem+json，业务扩展字段（issues、currentText 等）平铺在顶层
export interface Problem {
  title?: string
  status: number
  detail?: string
  errors?: { message: string; location?: string; value?: unknown }[]
  issues?: Issue[]
  currentText?: string
  currentHash?: string
  runningJobId?: string
}

export class ApiError extends Error {
  readonly status: number
  readonly problem: Problem

  constructor(problem: Problem) {
    super(problem.detail || problem.title || `HTTP ${problem.status}`)
    this.status = problem.status
    this.problem = problem
  }
}

let onUnauthorized: (() => void) | null = null

export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn
}

export async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`/api/v1${path}`, {
    method,
    credentials: 'same-origin',
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (res.status === 204) return undefined as T
  const isJson = res.headers.get('content-type')?.includes('json')
  const data = isJson ? await res.json() : await res.text()
  if (!res.ok) {
    if (res.status === 401 && !path.startsWith('/auth/login')) onUnauthorized?.()
    const problem: Problem =
      typeof data === 'object' && data ? { ...data, status: res.status } : { status: res.status, detail: String(data) }
    throw new ApiError(problem)
  }
  return data as T
}
