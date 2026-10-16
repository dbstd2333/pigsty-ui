import { useMemo } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { Button, Card, Dropdown, Empty, Space, Table, Tag, Tooltip, Typography } from 'antd'
import { DatabaseOutlined, PlusOutlined, ThunderboltOutlined } from '@ant-design/icons'
import { useStatusClusters } from '@/api/queries'
import { HealthBadge } from '@/features/status/HealthBadge'
import { usePendingPlans } from '@/features/actions/usePending'
import { compareIp, MODULE_COLORS, type GroupModel } from '@/features/config/model'
import { useSavedModel } from '@/features/config/useSavedModel'

export const Route = createFileRoute('/_app/clusters/')({
  component: ClustersPage,
})

function ClustersPage() {
  const { model, config } = useSavedModel()
  const status = useStatusClusters()
  const { plans } = usePendingPlans()
  const statusMap = useMemo(() => new Map((status.data ?? []).map((s) => [s.cluster, s])), [status.data])

  const pg = model?.groups.filter((g) => g.module === 'PGSQL') ?? []
  const redis = model?.groups.filter((g) => g.module === 'REDIS') ?? []
  const others = model?.groups.filter((g) => g.module !== 'PGSQL' && g.module !== 'REDIS') ?? []

  const pending = (g: GroupModel) => {
    const p = plans.get(g.name)
    if (!p?.changed) return null
    return (
      <Tooltip title={p.suggestions.map((s) => s.reason).join('；') || p.notes.map((n) => n.message).join('；')}>
        <Tag color="orange" icon={<ThunderboltOutlined />}>
          {p.applied ? `${p.suggestions.length} 项待应用` : '未部署'}
        </Tag>
      </Tooltip>
    )
  }

  return (
    <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr)', gap: 12 }}>
      <Card
        size="small"
        title={<Space><DatabaseOutlined />PostgreSQL 集群</Space>}
        loading={config.isLoading}
        extra={
          <Dropdown
            menu={{
              items: [
                { key: 'pgsql', label: <Link to="/clusters/new" search={{ kind: 'pgsql' }}>PostgreSQL 集群</Link> },
                { key: 'redis', label: <Link to="/clusters/new" search={{ kind: 'redis' }}>Redis 集群</Link> },
              ],
            }}
          >
            <Button type="primary" icon={<PlusOutlined />}>
              新建集群
            </Button>
          </Dropdown>
        }
      >
        <Table<GroupModel>
          size="small"
          rowKey="name"
          pagination={false}
          dataSource={pg}
          locale={{ emptyText: <Empty description="还没有 PostgreSQL 集群" /> }}
          columns={[
            {
              title: '集群',
              render: (_, g) => (
                <Space>
                  <Link to="/clusters/$cls" params={{ cls: g.name }}>
                    {g.name}
                  </Link>
                  {pending(g)}
                </Space>
              ),
            },
            { title: '状态', width: 160, render: (_, g) => <HealthBadge status={statusMap.get(g.name)} /> },
            {
              title: '成员',
              render: (_, g) => {
                const st = statusMap.get(g.name)
                return (
                  <Space size={4} wrap>
                    {Object.entries(g.hosts)
                      .sort(([a], [b]) => compareIp(a, b))
                      .map(([ip, hv]) => {
                        const m = st?.members.find((x) => x.host === ip)
                        const leader = m ? m.role === 'leader' || m.role === 'standby_leader' : hv.pg_role === 'primary'
                        return (
                          <Tooltip key={ip} title={m ? `${m.name} · ${m.role} · ${m.state}` : `配置角色：${String(hv.pg_role ?? '-')}`}>
                            <Tag color={leader ? 'blue' : undefined} className="mono" style={{ marginInlineEnd: 0 }}>
                              {ip}
                              {m && m.state !== 'running' && m.state !== 'streaming' ? ' ⚠' : ''}
                            </Tag>
                          </Tooltip>
                        )
                      })}
                  </Space>
                )
              },
            },
            { title: '模板', width: 90, render: (_, g) => String(g.vars.pg_conf ?? model?.globalVars.pg_conf ?? 'oltp.yml').replace('.yml', '') },
            { title: '版本', width: 70, render: (_, g) => String(g.vars.pg_version ?? model?.globalVars.pg_version ?? '-') },
            {
              title: '用户 / 库',
              width: 100,
              render: (_, g) =>
                `${Array.isArray(g.vars.pg_users) ? g.vars.pg_users.length : 0} / ${Array.isArray(g.vars.pg_databases) ? g.vars.pg_databases.length : 0}`,
            },
            {
              title: '',
              width: 120,
              render: (_, g) => (
                <Space size={4}>
                  <Link to="/clusters/$cls/backup" params={{ cls: g.name }}>
                    备份
                  </Link>
                  <Link to="/config" search={{ scope: `group:${g.name}` }}>
                    配置
                  </Link>
                </Space>
              ),
            },
          ]}
        />
      </Card>

      <Card
        size="small"
        title="Redis 集群"
        extra={
          <Link to="/clusters/new" search={{ kind: 'redis' }}>
            <Button icon={<PlusOutlined />}>新建 Redis 集群</Button>
          </Link>
        }
      >
        <Table<GroupModel>
          size="small"
          rowKey="name"
          pagination={false}
          dataSource={redis}
          locale={{ emptyText: <Empty description="还没有 Redis 集群" /> }}
          columns={[
            {
              title: '集群',
              render: (_, g) => (
                <Space>
                  <Link to="/clusters/$cls" params={{ cls: g.name }}>
                    {g.name}
                  </Link>
                  {pending(g)}
                </Space>
              ),
            },
            { title: '模式', width: 110, render: (_, g) => <Tag>{String(g.vars.redis_mode ?? 'standalone')}</Tag> },
            {
              title: '节点 / 实例',
              render: (_, g) => (
                <Space size={4} wrap>
                  {Object.entries(g.hosts).map(([ip, hv]) => (
                    <Tag key={ip} className="mono">
                      {ip} × {hv.redis_instances && typeof hv.redis_instances === 'object' ? Object.keys(hv.redis_instances).length : 0}
                    </Tag>
                  ))}
                </Space>
              ),
            },
            { title: '最大内存', width: 100, render: (_, g) => String(g.vars.redis_max_memory ?? model?.globalVars.redis_max_memory ?? '1GB') },
            {
              title: '',
              width: 80,
              render: (_, g) => (
                <Link to="/config" search={{ scope: `group:${g.name}` }}>
                  配置
                </Link>
              ),
            },
          ]}
        />
      </Card>

      <Card size="small" title="其他模块">
        <Space wrap>
          {others.map((g) => (
            <Link key={g.name} to="/config" search={{ scope: `group:${g.name}` }}>
              <Tag color={g.module ? MODULE_COLORS[g.module] : undefined}>
                {g.name} · {g.module ?? '未识别'} · {Object.keys(g.hosts).length} 台
              </Tag>
            </Link>
          ))}
          {others.length === 0 && <Typography.Text type="secondary">无</Typography.Text>}
        </Space>
      </Card>
    </div>
  )
}
