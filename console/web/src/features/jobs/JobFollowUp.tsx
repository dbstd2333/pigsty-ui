import { useNavigate, Link } from '@tanstack/react-router'
import { Alert, Button, Space } from 'antd'
import type { Job } from '@/api/types'
import { ActionButton } from '@/features/actions/RunAction'
import { editDraft } from '@/features/config/useConfigDraft'
import { useSavedModel } from '@/features/config/useSavedModel'
import { deleteIn } from '@/features/config/yamlPatch'

/** 作业成功后的下一步：移除实例后删配置、PITR 暂停后验证与提升、切换后回写角色 */
export function JobFollowUp({ job }: { job: Job }) {
  const { model, config } = useSavedModel()
  const navigate = useNavigate()
  if (job.status !== 'success' || job.check) return null
  const cls = String(job.params.cluster ?? job.cluster ?? '')

  if (job.action === 'pgsql-rm-replica') {
    const ips = (Array.isArray(job.params.ips) ? job.params.ips : []).map(String)
    const group = model?.groups.find((g) => g.name === cls)
    const left = ips.filter((ip) => group && ip in group.hosts)
    if (!left.length || !config.data) return null
    return (
      <Alert
        type="success"
        showIcon
        title={`实例已移除。现在可以从配置中删除 ${left.join('、')}。`}
        action={
          <Button
            type="primary"
            size="small"
            onClick={() => {
              editDraft(config.data!, (t) => left.reduce((acc, ip) => deleteIn(acc, ['all', 'children', cls, 'hosts', ip]), t))
              navigate({ to: '/config', search: { scope: `group:${cls}` } })
            }}
          >
            从配置中删除并去保存
          </Button>
        }
      />
    )
  }

  if ((job.action === 'pitr-clone' || job.action === 'pitr-inplace') && job.params.action === 'pause') {
    return (
      <Alert
        type="warning"
        showIcon
        title="恢复已完成并暂停在目标位置，请验证数据"
        description={
          <div style={{ display: 'grid', gap: 8 }}>
            <span>
              用只读连接核对关键表的行数和最近写入时间（例如 <code>psql -h &lt;主库IP&gt; -c &quot;SELECT count(*), max(updated_at) FROM ...&quot;</code>）。
              console 不会读取业务数据。确认无误后提升；如果目标不对，可以调整后重新恢复。
            </span>
            <Space>
              <ActionButton type="primary" action="pitr-promote" params={{ cluster: cls }}>
                确认提升
              </ActionButton>
              <Link to="/clusters/$cls/pitr" params={{ cls: String(job.params.source ?? cls) }}>
                <Button>调整目标重新恢复</Button>
              </Link>
            </Space>
          </div>
        }
      />
    )
  }

  if (job.action === 'pitr-promote' || ((job.action === 'pitr-clone' || job.action === 'pitr-inplace') && job.params.action === 'promote')) {
    return (
      <Alert
        type="success"
        showIcon
        title="集群已提升。建议立即做一次全量备份，为新时间线建立基线。"
        description={job.action === 'pitr-inplace' || job.params.backup ? '原地恢复保留的 /pg/data-backup 会占用磁盘，确认无误后请手动清理。' : undefined}
        action={
          <ActionButton type="primary" size="small" action="backup-run" params={{ cluster: cls, mode: 'full' }}>
            全量备份
          </ActionButton>
        }
      />
    )
  }

  if (job.action === 'pg-switchover') {
    return (
      <Alert
        type="info"
        showIcon
        title="主从切换完成。配置中的 pg_role 仍是旧的主库，建议回写，保持配置与实际一致。"
        action={
          <Link to="/clusters/$cls" params={{ cls }}>
            <Button size="small">去集群页回写 pg_role</Button>
          </Link>
        }
      />
    )
  }

  return null
}
