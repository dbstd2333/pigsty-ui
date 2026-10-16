import { Badge, Tooltip } from 'antd'
import type { ClusterStatus } from '@/api/types'

export function HealthBadge({ status }: { status?: ClusterStatus }) {
  if (!status) return <Badge status="default" text="—" />
  if (!status.source.ok)
    return (
      <Tooltip title={status.source.error}>
        <Badge status="default" text="未知" />
      </Tooltip>
    )
  if (status.members.length === 0) return <Badge status="default" text="未初始化" />
  if (status.healthy) return <Badge status="success" text={status.paused ? '健康（维护模式）' : '健康'} />
  return <Badge status="error" text="异常" />
}
