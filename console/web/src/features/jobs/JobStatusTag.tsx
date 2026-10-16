import { Tag } from 'antd'
import { CheckCircleOutlined, ClockCircleOutlined, CloseCircleOutlined, MinusCircleOutlined, SyncOutlined } from '@ant-design/icons'
import type { JobStatus } from '@/api/types'

const META: Record<JobStatus, { color: string; label: string; icon: React.ReactNode }> = {
  queued: { color: 'default', label: '排队中', icon: <ClockCircleOutlined /> },
  running: { color: 'processing', label: '运行中', icon: <SyncOutlined spin /> },
  success: { color: 'success', label: '成功', icon: <CheckCircleOutlined /> },
  failed: { color: 'error', label: '失败', icon: <CloseCircleOutlined /> },
  canceled: { color: 'warning', label: '已取消', icon: <MinusCircleOutlined /> },
}

export function JobStatusTag({ status, check }: { status: JobStatus; check?: boolean }) {
  const m = META[status] ?? META.queued
  return (
    <>
      <Tag color={m.color} icon={m.icon}>
        {m.label}
      </Tag>
      {check && <Tag>预演</Tag>}
    </>
  )
}

export const TASK_COLORS = {
  ok: '#52c41a',
  changed: '#faad14',
  failed: '#ff4d4f',
  unreachable: '#a8071a',
  skipped: '#d9d9d9',
} as const

export function duration(start: string | null, end: string | null) {
  if (!start) return '-'
  const ms = (end ? new Date(end).getTime() : Date.now()) - new Date(start).getTime()
  const s = Math.max(0, Math.round(ms / 1000))
  return s < 60 ? `${s} 秒` : s < 3600 ? `${Math.floor(s / 60)} 分 ${s % 60} 秒` : `${Math.floor(s / 3600)} 时 ${Math.floor((s % 3600) / 60)} 分`
}
