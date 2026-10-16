import { useEffect, useMemo, useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { qk } from '@/api/queries'
import type { HostStats, Job, JobEvent, TaskStatus } from '@/api/types'

export interface TaskRow {
  key: string
  step: number
  playbook: string
  play: string
  task: string
  results: Record<string, { status: TaskStatus; msg?: string; durationMs?: number }>
}

const MAX_LOG = 20_000

/**
 * 订阅作业事件（SSE）。断线时 EventSource 会自动重连，重连时带上已收到的最大 seq，
 * 由后端补发缺失的事件。作业结束后服务端发送 end 并关闭。
 */
export function useJobStream(id: string, initial?: Job) {
  const queryClient = useQueryClient()
  // 状态按作业 id 隔离：同一页面内从一个作业跳到另一个作业时不会串数据
  const [state, setState] = useState<{ id: string; job?: Job; events: JobEvent[] }>({ id, events: [] })
  const own = state.id === id ? state : { id, job: undefined, events: [] as JobEvent[] }
  const job = own.job ?? initial
  const events = own.events
  const setJob = (j: Job) => setState((s) => (s.id === id ? { ...s, job: j } : { id, job: j, events: [] }))
  const setEvents = (fn: (prev: JobEvent[]) => JobEvent[]) => setState((s) => (s.id === id ? { ...s, events: fn(s.events) } : { id, events: fn([]) }))
  const [connected, setConnected] = useState(false)
  const seqRef = useRef(0)

  useEffect(() => {
    seqRef.current = 0
    let es: EventSource | null = null
    let closed = false
    let buffer: JobEvent[] = []
    let timer: number | undefined

    // 合并批量事件，避免逐条重渲染
    const flush = () => {
      timer = undefined
      if (!buffer.length) return
      const batch = buffer
      buffer = []
      setEvents((prev) => {
        const next = prev.concat(batch)
        return next.length > MAX_LOG * 2 ? next.slice(-MAX_LOG * 2) : next
      })
    }

    const open = () => {
      es = new EventSource(`/api/v1/jobs/${encodeURIComponent(id)}/events?after=${seqRef.current}`)
      es.onopen = () => setConnected(true)
      es.addEventListener('job', (m) => {
        const e = JSON.parse((m as MessageEvent).data) as JobEvent
        if (e.seq <= seqRef.current) return
        seqRef.current = e.seq
        if (e.type === 'status' && e.job) setJob(e.job)
        buffer.push(e)
        timer ??= window.setTimeout(flush, 150)
      })
      es.addEventListener('end', (m) => {
        const j = JSON.parse((m as MessageEvent).data) as Job
        setJob(j)
        closed = true
        es?.close()
        setConnected(false)
        flush()
        queryClient.setQueryData(qk.job(id), j)
        queryClient.invalidateQueries({ queryKey: ['jobs'] })
        queryClient.invalidateQueries({ queryKey: qk.applied })
        queryClient.invalidateQueries({ queryKey: ['backups'] })
      })
      es.onerror = () => {
        setConnected(false)
        if (closed) es?.close()
      }
    }
    open()
    return () => {
      closed = true
      es?.close()
      if (timer) clearTimeout(timer)
    }
    // setJob/setEvents 只依赖 id
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id, queryClient])

  const derived = useMemo(() => {
    const tasks: TaskRow[] = []
    const byId = new Map<string, TaskRow>()
    const hosts = new Set<string>()
    const logs: string[] = []
    const steps: Record<number, { exitCode?: number; started: boolean }> = {}
    let stats: Record<string, HostStats> = {}
    let playbook = ''
    let play = ''
    let step = 0
    for (const e of events) {
      switch (e.type) {
        case 'step_start':
          step = e.step ?? step
          steps[step] = { started: true }
          break
        case 'step_end':
          steps[e.step ?? step] = { started: true, exitCode: e.exitCode }
          break
        case 'playbook_start':
          playbook = e.playbook ?? ''
          break
        case 'play_start':
          play = e.play ?? ''
          break
        case 'task_start': {
          const key = `${step}:${e.taskId ?? e.task}`
          if (!byId.has(key)) {
            const row = { key, step, playbook, play, task: e.task ?? '', results: {} }
            byId.set(key, row)
            tasks.push(row)
          }
          break
        }
        case 'runner_result': {
          if (e.host) hosts.add(e.host)
          const row = byId.get(`${step}:${e.taskId ?? e.task}`)
          if (row && e.host && e.status) row.results[e.host] = { status: e.status, msg: e.msg, durationMs: e.durationMs }
          break
        }
        case 'stats':
          stats = { ...stats, ...(e.hosts ?? {}) }
          for (const h of Object.keys(e.hosts ?? {})) hosts.add(h)
          break
        case 'log':
          if (e.line !== undefined) logs.push(e.line)
          break
      }
    }
    return { tasks, hosts: [...hosts], logs: logs.slice(-MAX_LOG), steps, stats }
  }, [events])

  return { job, connected, events, ...derived }
}
