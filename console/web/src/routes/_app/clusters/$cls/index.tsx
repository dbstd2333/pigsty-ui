import { useMemo } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import {
  Alert,
  App,
  Button,
  Card,
  Col,
  Descriptions,
  Empty,
  Result,
  Row,
  Space,
  Spin,
  Table,
  Tabs,
  Tag,
  Tooltip,
  Typography,
} from 'antd'
import { EditOutlined, LineChartOutlined, SaveOutlined } from '@ant-design/icons'
import { useConfig, useDrift, usePgConfig, useSchema, useStatusCluster } from '@/api/queries'
import type { Drift, PatroniMember } from '@/api/types'
import { ActionButton } from '@/features/actions/RunAction'
import { usePendingPlans } from '@/features/actions/usePending'
import { compareIp, MODULE_COLORS, type GroupModel } from '@/features/config/model'
import { editDraft } from '@/features/config/useConfigDraft'
import { useSavedModel } from '@/features/config/useSavedModel'
import { setIn } from '@/features/config/yamlPatch'
import { estimateNode, pgParams, pgConfName } from '@/features/memory/estimate'
import { useSpecsMap } from '@/features/memory/useSpecs'
import { useGrafana } from '@/features/status/grafana'
import { HealthBadge } from '@/features/status/HealthBadge'

export const Route = createFileRoute('/_app/clusters/$cls/')({
  component: ClusterDetail,
})

function ClusterDetail() {
  const { cls } = Route.useParams()
  const { model, config } = useSavedModel()
  const group = model?.groups.find((g) => g.name === cls)

  if (config.isLoading) return <Spin style={{ display: 'block', marginTop: 80 }} />
  if (!group)
    return <Result status="404" title={`配置中没有集群 ${cls}`} extra={<Link to="/clusters">返回集群列表</Link>} />
  if (group.module === 'REDIS') return <RedisDetail group={group} />
  return <PgDetail group={group} />
}

function Header({ group, extra }: { group: GroupModel; extra?: React.ReactNode }) {
  return (
    <Space style={{ justifyContent: 'space-between', width: '100%' }} wrap>
      <Space>
        <Typography.Title level={4} style={{ margin: 0 }}>
          {group.name}
        </Typography.Title>
        {group.module && <Tag color={MODULE_COLORS[group.module]}>{group.module}</Tag>}
      </Space>
      <Space wrap>
        {extra}
        <Link to="/config" search={{ scope: `group:${group.name}` }}>
          <Button icon={<EditOutlined />}>编辑配置</Button>
        </Link>
      </Space>
    </Space>
  )
}

function PendingCard({ cls }: { cls: string }) {
  const { plans, loading } = usePendingPlans()
  const plan = plans.get(cls)
  return (
    <Card
      size="small"
      title="待应用变更"
      extra={
        plan?.applied && (
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            {plan.applied.baseline ? '基线' : '最近应用'}版本 <span className="mono">{plan.applied.commit.slice(0, 8)}</span>
          </Typography.Text>
        )
      }
    >
      {loading ? (
        <Spin />
      ) : !plan?.changed ? (
        <Typography.Text type="secondary">配置与最近一次执行时一致，没有待应用的变更。</Typography.Text>
      ) : (
        <div style={{ display: 'grid', gap: 8 }}>
          {!plan.applied && <Alert type="info" showIcon title="这是新集群，尚未通过 console 部署。按顺序执行下面的动作。" />}
          {plan.suggestions.map((s, i) => (
            <Space key={s.key} style={{ justifyContent: 'space-between', width: '100%' }}>
              <span>
                <Tag>{i + 1}</Tag>
                {s.reason}
                <Typography.Text type="secondary" className="mono" style={{ marginInlineStart: 8, fontSize: 12 }}>
                  {s.action}
                </Typography.Text>
              </span>
              <ActionButton size="small" type="primary" ghost action={s.action} params={s.params}>
                执行
              </ActionButton>
            </Space>
          ))}
          {plan.notes.map((n) => (
            <Alert key={n.message} type={n.level === 'warning' ? 'warning' : 'info'} showIcon title={n.message} />
          ))}
        </div>
      )}
    </Card>
  )
}

// ---------- PostgreSQL ----------

function PgDetail({ group }: { group: GroupModel }) {
  const cls = group.name
  const status = useStatusCluster(cls)
  const drift = useDrift()
  const grafana = useGrafana()
  const st = status.data
  const myDrift = (drift.data ?? []).filter((d) => d.cluster === cls)
  const hosts = Object.keys(group.hosts).sort(compareIp)
  const leader = st?.leader ?? hosts.find((ip) => group.hosts[ip].pg_role === 'primary')

  const users = Array.isArray(group.vars.pg_users) ? (group.vars.pg_users as Record<string, unknown>[]) : []
  const dbs = Array.isArray(group.vars.pg_databases) ? (group.vars.pg_databases as Record<string, unknown>[]) : []

  return (
    <div style={{ display: 'grid', gap: 12 }}>
      <Header
        group={group}
        extra={
          <>
            {grafana.cluster(cls) && (
              <Button icon={<LineChartOutlined />} href={grafana.cluster(cls)} target="_blank">
                Grafana
              </Button>
            )}
            <Link to="/clusters/$cls/backup" params={{ cls }}>
              <Button icon={<SaveOutlined />}>备份与恢复</Button>
            </Link>
          </>
        }
      />
      {myDrift.map((d) => (
        <DriftAlert key={`${d.kind}:${d.host}`} d={d} />
      ))}
      <Tabs
        defaultActiveKey="members"
        items={[
          {
            key: 'members',
            label: '成员与拓扑',
            children: (
              <div style={{ display: 'grid', gap: 12 }}>
                <Card
                  size="small"
                  title={
                    <Space>
                      成员 <HealthBadge status={st} />
                      {st?.paused && <Tag color="warning">维护模式</Tag>}
                    </Space>
                  }
                  extra={
                    <Space>
                      <ActionButton size="small" action="pgsql-add-replica" params={{ cluster: cls }}>
                        添加从库
                      </ActionButton>
                      <ActionButton size="small" action="pg-switchover" params={{ cluster: cls }} danger>
                        主从切换
                      </ActionButton>
                    </Space>
                  }
                >
                  {st && !st.source.ok && <Alert type="warning" showIcon style={{ marginBottom: 8 }} title={`无法获取 Patroni 状态：${st.source.error ?? '未知错误'}`} />}
                  <MembersTable group={group} members={st?.members ?? []} leader={leader} />
                </Card>
                <PendingCard cls={cls} />
              </div>
            ),
          },
          {
            key: 'users',
            label: `用户（${users.length}）`,
            children: (
              <Card size="small" extra={<Link to="/config" search={{ scope: `group:${cls}` }}>在工作台编辑</Link>}>
                <Table
                  size="small"
                  rowKey={(u) => String(u.name)}
                  pagination={false}
                  dataSource={users}
                  columns={[
                    { title: '用户', render: (_, u) => <span className="mono">{String(u.name)}</span> },
                    { title: '角色', render: (_, u) => (Array.isArray(u.roles) ? u.roles.map((r) => <Tag key={String(r)}>{String(r)}</Tag>) : null) },
                    { title: '连接池', width: 70, render: (_, u) => (u.pgbouncer ? '是' : '否') },
                    { title: '状态', width: 80, render: (_, u) => (u.state === 'absent' ? <Tag color="red">待删除</Tag> : null) },
                    { title: '备注', render: (_, u) => String(u.comment ?? '') },
                    {
                      width: 110,
                      render: (_, u) => (
                        <ActionButton size="small" action="pgsql-user" params={{ cluster: cls, user: u.name }} danger={u.state === 'absent'}>
                          执行
                        </ActionButton>
                      ),
                    },
                  ]}
                />
              </Card>
            ),
          },
          {
            key: 'dbs',
            label: `数据库（${dbs.length}）`,
            children: (
              <Card size="small" extra={<Link to="/config" search={{ scope: `group:${cls}` }}>在工作台编辑</Link>}>
                <Table
                  size="small"
                  rowKey={(d) => String(d.name)}
                  pagination={false}
                  dataSource={dbs}
                  columns={[
                    { title: '数据库', render: (_, d) => <span className="mono">{String(d.name)}</span> },
                    { title: '属主', render: (_, d) => String(d.owner ?? '-') },
                    {
                      title: '扩展',
                      render: (_, d) =>
                        Array.isArray(d.extensions)
                          ? d.extensions.map((e) => <Tag key={JSON.stringify(e)}>{typeof e === 'string' ? e : String((e as { name?: string }).name)}</Tag>)
                          : null,
                    },
                    { title: '状态', width: 80, render: (_, d) => (d.state === 'absent' ? <Tag color="red">待删除</Tag> : null) },
                    { title: '备注', render: (_, d) => String(d.comment ?? '') },
                    {
                      width: 110,
                      render: (_, d) => (
                        <ActionButton size="small" action="pgsql-db" params={{ cluster: cls, db: d.name }} danger={d.state === 'absent'}>
                          执行
                        </ActionButton>
                      ),
                    },
                  ]}
                />
              </Card>
            ),
          },
          {
            key: 'access',
            label: '访问与服务',
            children: <AccessTab group={group} />,
          },
          {
            key: 'params',
            label: '参数与内存',
            children: <ParamsTab group={group} leader={leader} />,
          },
        ]}
      />
    </div>
  )
}

function DriftAlert({ d }: { d: Drift }) {
  const config = useConfig()
  const navigate = useNavigate()
  const { modal } = App.useApp()
  const fix = () => {
    if (!d.fix || !config.data) return
    const f = d.fix
    modal.confirm({
      title: '回写 pg_role',
      content: `将 ${f.primary} 改为 primary，${f.replicas.join('、')} 改为 replica。修改会写入草稿，在工作台核对后保存。`,
      onOk: () => {
        editDraft(config.data!, (t) => {
          let out = setIn(t, ['all', 'children', d.cluster, 'hosts', f.primary, 'pg_role'], 'primary')
          for (const r of f.replicas) out = setIn(out, ['all', 'children', d.cluster, 'hosts', r, 'pg_role'], 'replica')
          return out
        })
        navigate({ to: '/config', search: { scope: `group:${d.cluster}` } })
      },
    })
  }
  return (
    <Alert
      type="warning"
      showIcon
      title={d.message}
      action={
        d.fix && (
          <Button size="small" onClick={fix}>
            回写 pg_role
          </Button>
        )
      }
    />
  )
}

function MembersTable({ group, members, leader }: { group: GroupModel; members: PatroniMember[]; leader?: string }) {
  const grafana = useGrafana()
  const cls = group.name
  const rows = useMemo(() => {
    const ips = new Set([...Object.keys(group.hosts), ...members.map((m) => m.host)])
    return [...ips].sort(compareIp).map((ip) => ({ ip, conf: group.hosts[ip], m: members.find((x) => x.host === ip) }))
  }, [group, members])

  return (
    <Table
      size="small"
      rowKey="ip"
      pagination={false}
      dataSource={rows}
      columns={[
        {
          title: '实例',
          render: (_, r) => (
            <Space direction="vertical" size={0}>
              <span className="mono">{r.m?.name ?? (r.conf ? `${cls}-${String(r.conf.pg_seq ?? '?')}` : '-')}</span>
              <Typography.Text type="secondary" className="mono" style={{ fontSize: 12 }}>
                {r.ip}
                {r.m ? `:${r.m.port}` : ''}
              </Typography.Text>
            </Space>
          ),
        },
        {
          title: '配置角色',
          width: 100,
          render: (_, r) => (r.conf ? <Tag>{String(r.conf.pg_role ?? '-')}</Tag> : <Tag color="red">不在配置中</Tag>),
        },
        {
          title: '实际角色',
          width: 120,
          render: (_, r) =>
            r.m ? <Tag color={r.m.role === 'leader' || r.m.role === 'standby_leader' ? 'blue' : r.m.role === 'sync_standby' ? 'cyan' : undefined}>{r.m.role}</Tag> : '-',
        },
        {
          title: '状态',
          width: 110,
          render: (_, r) => (r.m ? <Tag color={r.m.state === 'running' || r.m.state === 'streaming' ? 'success' : 'error'}>{r.m.state}</Tag> : <Typography.Text type="secondary">无</Typography.Text>),
        },
        { title: '时间线', width: 70, render: (_, r) => r.m?.timeline ?? '-' },
        {
          title: '复制延迟',
          width: 100,
          render: (_, r) => (r.m?.lagBytes === null || r.m?.lagBytes === undefined ? '-' : r.m.lagBytes === 0 ? '0' : `${(r.m.lagBytes / 1024 / 1024).toFixed(1)} MB`),
        },
        {
          title: '',
          width: 70,
          render: (_, r) => r.m?.pendingRestart && <Tooltip title="有参数需要重启后才生效"><Tag color="orange">待重启</Tag></Tooltip>,
        },
        {
          title: '操作',
          width: 280,
          render: (_, r) => (
            <Space size={4}>
              {r.m && (
                <ActionButton size="small" action="pg-restart" params={{ cluster: cls, member: r.ip }}>
                  重启
                </ActionButton>
              )}
              {r.conf && r.ip !== leader && (
                <ActionButton size="small" danger action="pgsql-rm-replica" params={{ cluster: cls, ips: [r.ip] }}>
                  移除实例
                </ActionButton>
              )}
              {r.m && grafana.instance(r.m.name) && (
                <Button size="small" type="link" href={grafana.instance(r.m.name)} target="_blank">
                  监控
                </Button>
              )}
            </Space>
          ),
        },
      ]}
    />
  )
}

function AccessTab({ group }: { group: GroupModel }) {
  const cls = group.name
  const hba = Array.isArray(group.vars.pg_hba_rules) ? (group.vars.pg_hba_rules as Record<string, unknown>[]) : []
  const svcs = Array.isArray(group.vars.pg_services) ? (group.vars.pg_services as Record<string, unknown>[]) : []
  const exts = Array.isArray(group.vars.pg_extensions) ? (group.vars.pg_extensions as unknown[]) : []
  return (
    <Row gutter={12}>
      <Col span={12}>
        <Card
          size="small"
          title={`HBA 规则（${hba.length}）`}
          extra={
            <ActionButton size="small" action="pgsql-hba" params={{ cluster: cls }}>
              刷新 HBA
            </ActionButton>
          }
        >
          {hba.length ? (
            <Table
              size="small"
              pagination={false}
              rowKey={(_, i) => String(i)}
              dataSource={hba}
              columns={[
                { title: '用户', render: (_, r) => <span className="mono">{String(r.user ?? '-')}</span> },
                { title: '数据库', render: (_, r) => <span className="mono">{String(r.db ?? 'all')}</span> },
                { title: '来源', render: (_, r) => <span className="mono">{String(r.addr ?? '-')}</span> },
                { title: '认证', render: (_, r) => String(r.auth ?? '-') },
                { title: '说明', render: (_, r) => String(r.title ?? '') },
              ]}
            />
          ) : (
            <Empty description="只使用默认 HBA 规则" image={Empty.PRESENTED_IMAGE_SIMPLE} />
          )}
        </Card>
      </Col>
      <Col span={12}>
        <div style={{ display: 'grid', gap: 12 }}>
          <Card
            size="small"
            title={`自定义服务（${svcs.length}）`}
            extra={
              <ActionButton size="small" action="pgsql-svc" params={{ cluster: cls }}>
                刷新服务
              </ActionButton>
            }
          >
            {svcs.length ? (
              <Space wrap>
                {svcs.map((s) => (
                  <Tag key={String(s.name)}>
                    {String(s.name)} :{String(s.port)} → {String(s.dest ?? 'default')}
                  </Tag>
                ))}
              </Space>
            ) : (
              <Typography.Text type="secondary">只使用默认服务（primary / replica / default / offline）</Typography.Text>
            )}
          </Card>
          <Card
            size="small"
            title={`扩展软件包（${exts.length}）`}
            extra={
              <ActionButton size="small" action="pgsql-ext" params={{ cluster: cls }}>
                安装扩展
              </ActionButton>
            }
          >
            <Space wrap>
              {exts.map((e) => (
                <Tag key={String(e)}>{String(e)}</Tag>
              ))}
              {exts.length === 0 && <Typography.Text type="secondary">无</Typography.Text>}
            </Space>
          </Card>
          <Card size="small" title="定时任务">
            <ActionButton size="small" action="pgsql-crontab" params={{ cluster: cls }}>
              刷新 pg_crontab
            </ActionButton>
          </Card>
        </div>
      </Col>
    </Row>
  )
}

const COMPARE_KEYS: { key: keyof ReturnType<typeof pgParams>; param: string; unit: 'MB' | '' }[] = [
  { key: 'maxConnections', param: 'max_connections', unit: '' },
  { key: 'sharedBuffers', param: 'shared_buffers', unit: 'MB' },
  { key: 'workMem', param: 'work_mem', unit: 'MB' },
  { key: 'maintenanceWorkMem', param: 'maintenance_work_mem', unit: 'MB' },
  { key: 'effectiveCacheSize', param: 'effective_cache_size', unit: 'MB' },
]

/** PG 内存参数：按配置推导的值 vs Patroni DCS 中实际生效的值（DESIGN §12.4） */
function ParamsTab({ group, leader }: { group: GroupModel; leader?: string }) {
  const cls = group.name
  const pgc = usePgConfig(cls)
  const { model } = useSavedModel()
  const schema = useSchema()
  const { specs, baseline } = useSpecsMap()
  const spec = leader ? specs[leader] : undefined
  const vars = { ...model?.globalVars, ...group.vars }
  const derived = spec
    ? pgParams(spec.memMb, pgConfName(vars.pg_conf), vars.pg_max_conn ?? 'auto', Number(vars.pg_shared_buffer_ratio ?? 0.25), String(vars.pg_default_service_dest ?? 'pgbouncer'))
    : undefined
  const actual = pgc.data?.parameters ?? {}
  const toMb = (v: string | undefined) => {
    if (!v) return undefined
    const m = v.match(/^(\d+)\s*(kB|MB|GB|TB)?$/i)
    if (!m) return undefined
    const n = Number(m[1])
    const u = (m[2] ?? 'MB').toUpperCase()
    return u === 'KB' ? n / 1024 : u === 'GB' ? n * 1024 : u === 'TB' ? n * 1024 * 1024 : n
  }

  const rows = COMPARE_KEYS.map((c) => {
    const d = derived?.[c.key] as number | undefined
    const a = actual[c.param]
    const an = c.unit ? toMb(a) : a !== undefined ? Number(a) : undefined
    const diff = d !== undefined && an !== undefined && d > 0 ? Math.abs(an - d) / d : undefined
    return { ...c, derived: d, actual: a, diff }
  })

  return (
    <Row gutter={12}>
      <Col span={14}>
        <Card
          size="small"
          title="内存参数：配置推导 vs 实际生效"
          extra={
            <ActionButton size="small" action="patroni-param" params={{ cluster: cls }} lockParams>
              修改 Patroni 参数
            </ActionButton>
          }
        >
          {pgc.data && !pgc.data.source.ok && <Alert type="warning" showIcon style={{ marginBottom: 8 }} title={`无法读取 DCS 参数：${pgc.data.source.error}`} />}
          {!spec && <Alert type="info" showIcon style={{ marginBottom: 8 }} title={`主库 ${leader ?? ''} 规格未知，无法推导`} />}
          <Table
            size="small"
            rowKey="param"
            pagination={false}
            dataSource={rows}
            columns={[
              { title: '参数', dataIndex: 'param', render: (p) => <span className="mono">{p}</span> },
              { title: '按当前配置推导', render: (_, r) => (r.derived === undefined ? '-' : `${r.derived}${r.unit}`) },
              { title: '实际（DCS）', render: (_, r) => <span className="mono">{r.actual ?? '-'}</span> },
              {
                title: '',
                width: 120,
                render: (_, r) => r.diff !== undefined && r.diff > 0.3 && <Tag color="orange">差异 {(r.diff * 100).toFixed(0)}%</Tag>,
              },
            ]}
          />
          <Typography.Paragraph type="secondary" style={{ fontSize: 12, margin: '8px 0 0' }}>
            内存参数只在集群初始化时由主库按它自己的内存计算一次，写入 DCS 后全集群共用。之后修改 pg_conf / pg_shared_buffer_ratio / pg_max_conn 不会生效；差异大多是用 edit-config 改过，或初始化后才改了配置。
          </Typography.Paragraph>
        </Card>
      </Col>
      <Col span={10}>
        <Card size="small" title="成员内存预估" extra={<Link to="/capacity">容量页</Link>}>
          {model &&
            Object.keys(group.hosts)
              .sort(compareIp)
              .map((ip) => {
                const e = estimateNode(ip, { model, schema: schema.data, specs, baseline })
                return (
                  <Descriptions
                    key={ip}
                    size="small"
                    column={1}
                    title={<span className="mono" style={{ fontSize: 13 }}>{ip}</span>}
                    items={[
                      { label: '节点内存', children: e.spec ? `${Math.round(e.spec.memMb / 1024)} GB（${e.spec.source === 'metrics' ? '监控' : '规划'}）` : '未知' },
                      { label: '典型占用', children: e.spec ? `${Math.round(e.typical)} MB（${((e.typical / e.spec.memMb) * 100).toFixed(0)}%）` : '-' },
                      ...e.warnings.map((w, i) => ({ key: String(i), label: '提示', children: <Typography.Text type="warning">{w}</Typography.Text> })),
                    ]}
                  />
                )
              })}
        </Card>
      </Col>
    </Row>
  )
}

// ---------- Redis ----------

function RedisDetail({ group }: { group: GroupModel }) {
  const cls = group.name
  const mode = String(group.vars.redis_mode ?? 'standalone')
  const rows = Object.entries(group.hosts)
    .sort(([a], [b]) => compareIp(a, b))
    .flatMap(([ip, hv]) =>
      Object.entries((hv.redis_instances ?? {}) as Record<string, Record<string, unknown> | null>).map(([port, iv]) => ({
        key: `${ip}:${port}`,
        ip,
        node: hv.redis_node,
        port,
        replicaOf: iv?.replica_of ? String(iv.replica_of) : '',
      })),
    )
  const monitors = Array.isArray(group.vars.redis_sentinel_monitor) ? (group.vars.redis_sentinel_monitor as Record<string, unknown>[]) : []
  return (
    <div style={{ display: 'grid', gap: 12 }}>
      <Header
        group={group}
        extra={
          <ActionButton action="redis-create" params={{ cluster: cls }}>
            部署 / 重新应用
          </ActionButton>
        }
      />
      <Card size="small">
        <Descriptions
          size="small"
          column={4}
          items={[
            { label: '模式', children: <Tag>{mode}</Tag> },
            { label: '每实例最大内存', children: String(group.vars.redis_max_memory ?? '1GB') },
            { label: '淘汰策略', children: String(group.vars.redis_mem_policy ?? 'allkeys-lru') },
            { label: '认证', children: group.vars.redis_password ? '已设置密码' : '无' },
          ]}
        />
      </Card>
      <Card size="small" title={`实例（${rows.length}）`}>
        <Table
          size="small"
          pagination={false}
          dataSource={rows}
          columns={[
            { title: '节点', width: 70, render: (_, r) => String(r.node ?? '-') },
            { title: '实例', render: (_, r) => <span className="mono">{`${cls}-${String(r.node ?? '?')}-${r.port}`}</span> },
            { title: '地址', render: (_, r) => <span className="mono">{r.ip}:{r.port}</span> },
            { title: '复制自', render: (_, r) => (r.replicaOf ? <span className="mono">{r.replicaOf}</span> : mode === 'standalone' ? <Tag color="blue">主</Tag> : '-') },
          ]}
        />
      </Card>
      {mode === 'sentinel' && (
        <Card size="small" title="哨兵监控目标">
          <Space wrap>
            {monitors.map((m) => (
              <Tag key={String(m.name)}>
                {String(m.name)} → {String(m.host)}:{String(m.port)}（quorum {String(m.quorum ?? '-')}）
              </Tag>
            ))}
          </Space>
        </Card>
      )}
      <PendingCard cls={cls} />
    </div>
  )
}
