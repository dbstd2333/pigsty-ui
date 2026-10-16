import { useMemo } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useQueries } from '@tanstack/react-query'
import { Alert, Card, Col, Empty, Row, Space, Statistic, Table, Tag, Tooltip, Typography } from 'antd'
import { ThunderboltOutlined } from '@ant-design/icons'
import dayjs from 'dayjs'
import { api } from '@/api/endpoints'
import { qk, useAlerts, useDrift, useHistory, useJobs, useSettings, useStatusClusters, useStatusNodes } from '@/api/queries'
import type { BackupInfo } from '@/api/types'
import { usePendingPlans } from '@/features/actions/usePending'
import { duration, JobStatusTag } from '@/features/jobs/JobStatusTag'
import { allHosts, type GroupModel } from '@/features/config/model'
import { useSavedModel } from '@/features/config/useSavedModel'
import { HealthBadge } from '@/features/status/HealthBadge'

export const Route = createFileRoute('/_app/')({
  component: Overview,
})

function Overview() {
  const { model } = useSavedModel()
  const history = useHistory(6, 0)
  const jobs = useJobs({ limit: 8 })
  const status = useStatusClusters()
  const nodes = useStatusNodes()
  const drift = useDrift()
  const alerts = useAlerts()
  const settings = useSettings()
  const { plans } = usePendingPlans()

  const pg = model?.groups.filter((g) => g.module === 'PGSQL') ?? []
  const redis = model?.groups.filter((g) => g.module === 'REDIS') ?? []
  const statusMap = useMemo(() => new Map((status.data ?? []).map((s) => [s.cluster, s])), [status.data])
  const backups = useQueries({ queries: pg.map((g) => ({ queryKey: qk.backups(g.name), queryFn: () => api.backups(g.name), staleTime: 5 * 60_000 })) })
  const backupMap = new Map<string, BackupInfo>()
  backups.forEach((b, i) => b.data && backupMap.set(pg[i].name, b.data))

  const running = (jobs.data?.items ?? []).filter((j) => j.status === 'running' || j.status === 'queued')
  const restoring = running.filter((j) => j.action === 'pitr-inplace')
  const offline = (nodes.data?.items ?? []).filter((n) => n.managed && n.up === false)
  const maxAge = settings.data?.backupMaxAgeHours ?? 26

  return (
    <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr)', gap: 12 }}>
      {restoring.map((j) => (
        <Alert
          key={j.id}
          type="error"
          showIcon
          title={
            <span>
              集群 <b>{j.cluster}</b> 正在原地恢复（PITR），期间该集群不可用、其他变更已锁定。{' '}
              <Link to="/jobs/$id" params={{ id: j.id }}>
                查看进度
              </Link>
            </span>
          }
        />
      ))}
      {(drift.data ?? []).length > 0 && (
        <Alert
          type="warning"
          showIcon
          title={`发现 ${drift.data!.length} 处配置与实际状态不一致`}
          description={
            <ul style={{ margin: 0, paddingInlineStart: 18 }}>
              {drift.data!.map((d) => (
                <li key={`${d.cluster}:${d.host}:${d.kind}`}>
                  <Link to="/clusters/$cls" params={{ cls: d.cluster }}>
                    {d.cluster}
                  </Link>
                  ：{d.message}
                </li>
              ))}
            </ul>
          }
        />
      )}
      <Row gutter={12}>
        <Col span={6}>
          <Card size="small">
            <Statistic title="节点" value={model ? allHosts(model).length : '-'} suffix={offline.length ? <Typography.Text type="danger" style={{ fontSize: 14 }}>{offline.length} 离线</Typography.Text> : undefined} />
          </Card>
        </Col>
        <Col span={6}>
          <Card size="small">
            <Statistic title="PG 集群 / 实例" value={`${pg.length} / ${pg.reduce((n, g) => n + Object.keys(g.hosts).length, 0)}`} />
          </Card>
        </Col>
        <Col span={6}>
          <Card size="small">
            <Statistic title="Redis 集群" value={redis.length} />
          </Card>
        </Col>
        <Col span={6}>
          <Card size="small">
            <Statistic title="活跃告警" value={alerts.data?.items.length ?? '-'} valueStyle={{ color: alerts.data?.items.length ? '#cf1322' : undefined }} />
          </Card>
        </Col>
      </Row>
      <Row gutter={12}>
        <Col span={15}>
          <Card size="small" title="集群" extra={<Link to="/clusters">全部</Link>}>
            <Table<GroupModel>
              size="small"
              rowKey="name"
              pagination={false}
              dataSource={[...pg, ...redis]}
              locale={{ emptyText: <Empty description="还没有集群" /> }}
              columns={[
                {
                  title: '集群',
                  render: (_, g) => (
                    <Space size={4}>
                      <Link to="/clusters/$cls" params={{ cls: g.name }}>
                        {g.name}
                      </Link>
                      {g.module === 'REDIS' && <Tag color="red">REDIS</Tag>}
                    </Space>
                  ),
                },
                { title: '状态', width: 120, render: (_, g) => (g.module === 'PGSQL' ? <HealthBadge status={statusMap.get(g.name)} /> : '-') },
                {
                  title: '最近备份',
                  width: 130,
                  render: (_, g) => {
                    if (g.module !== 'PGSQL') return '-'
                    const b = backupMap.get(g.name)
                    const last = b?.backups.filter((x) => !x.error).at(-1)
                    if (!last) return <Typography.Text type="secondary">{b ? '无备份' : '…'}</Typography.Text>
                    const age = dayjs().diff(dayjs(last.stop), 'hour', true)
                    return (
                      <Tooltip title={dayjs(last.stop).format('YYYY-MM-DD HH:mm')}>
                        <Typography.Text type={age > maxAge ? 'danger' : undefined}>{dayjs(last.stop).fromNow()}</Typography.Text>
                      </Tooltip>
                    )
                  },
                },
                {
                  title: 'RPO',
                  width: 90,
                  render: (_, g) => {
                    const t = backupMap.get(g.name)?.archive.lastArchivedAt
                    if (!t) return '-'
                    const s = dayjs().diff(dayjs(t), 'second')
                    return <Typography.Text type={s > 900 ? 'danger' : s > 300 ? 'warning' : undefined}>{s < 120 ? `${s} 秒` : dayjs(t).fromNow(true)}</Typography.Text>
                  },
                },
                {
                  title: '待应用',
                  width: 100,
                  render: (_, g) => {
                    const p = plans.get(g.name)
                    return p?.changed ? (
                      <Tag color="orange" icon={<ThunderboltOutlined />}>
                        {p.applied ? `${p.suggestions.length} 项` : '未部署'}
                      </Tag>
                    ) : null
                  },
                },
              ]}
            />
          </Card>
        </Col>
        <Col span={9}>
          <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr)', gap: 12 }}>
            <Card size="small" title="最近作业" extra={<Link to="/jobs">全部</Link>}>
              {(jobs.data?.items ?? []).length === 0 && <Typography.Text type="secondary">暂无作业</Typography.Text>}
              {(jobs.data?.items ?? []).map((j) => (
                <div key={j.id} style={{ display: 'flex', justifyContent: 'space-between', padding: '4px 0', gap: 8 }}>
                  <Link to="/jobs/$id" params={{ id: j.id }} style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                    {j.title}
                  </Link>
                  <Space size={4} style={{ flexShrink: 0 }}>
                    <JobStatusTag status={j.status} />
                    <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                      {j.status === 'running' ? duration(j.startedAt, null) : dayjs(j.createdAt).fromNow()}
                    </Typography.Text>
                  </Space>
                </div>
              ))}
            </Card>
            <Card size="small" title="告警">
              {alerts.data && !alerts.data.source.ok && <Typography.Text type="secondary">无法连接 Alertmanager：{alerts.data.source.error}</Typography.Text>}
              {alerts.data?.source.ok && alerts.data.items.length === 0 && <Typography.Text type="secondary">没有活跃告警</Typography.Text>}
              {(alerts.data?.items ?? []).slice(0, 8).map((a) => (
                <div key={`${a.name}:${JSON.stringify(a.labels)}`} style={{ padding: '2px 0' }}>
                  <Tag color={a.severity === 'CRIT' || a.severity === 'critical' ? 'red' : 'orange'}>{a.severity}</Tag>
                  {a.summary || a.name}
                  <Typography.Text type="secondary" style={{ fontSize: 12, marginInlineStart: 6 }}>
                    {dayjs(a.startsAt).fromNow()}
                  </Typography.Text>
                </div>
              ))}
            </Card>
            <Card size="small" title="最近的配置变更" extra={<Link to="/config/history">全部</Link>}>
              {(history.data?.items ?? []).map((c) => (
                <div key={c.hash} style={{ padding: '3px 0' }}>
                  <div style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{c.message}</div>
                  <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                    {c.author === 'external' ? '外部修改' : c.author} · {dayjs(c.time).fromNow()}
                  </Typography.Text>
                </div>
              ))}
            </Card>
          </div>
        </Col>
      </Row>
    </div>
  )
}
