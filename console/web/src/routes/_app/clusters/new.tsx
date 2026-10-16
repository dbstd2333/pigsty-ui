import { useMemo, useState } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import {
  Alert,
  App,
  AutoComplete,
  Button,
  Card,
  Col,
  Divider,
  Form,
  Input,
  InputNumber,
  Radio,
  Row,
  Select,
  Space,
  Steps,
  Switch,
  Table,
  Tag,
  Typography,
} from 'antd'
import { DeleteOutlined, PlusOutlined, ReloadOutlined } from '@ant-design/icons'
import { useConfig, useSchema } from '@/api/queries'
import {
  pgGroup,
  randomPassword,
  redisGroup,
  validatePg,
  validateRedis,
  type PgSpec,
  type RedisMode,
  type RedisSpec,
  type SentinelMonitor,
} from '@/features/clusters/templates'
import { allHosts, compareIp, type ConfigModel } from '@/features/config/model'
import { editDraft, hasDraft } from '@/features/config/useConfigDraft'
import { useSavedModel } from '@/features/config/useSavedModel'
import { formatInline, setIn } from '@/features/config/yamlPatch'
import { estimateNode } from '@/features/memory/estimate'
import { LevelTag, MemoryBar } from '@/features/memory/MemoryBar'
import { useSpecsMap } from '@/features/memory/useSpecs'

type Kind = 'pgsql' | 'redis'

export const Route = createFileRoute('/_app/clusters/new')({
  validateSearch: (s: Record<string, unknown>): { kind: Kind } => ({ kind: s.kind === 'redis' ? 'redis' : 'pgsql' }),
  component: NewCluster,
})

function NewCluster() {
  const { kind } = Route.useSearch()
  const navigate = Route.useNavigate()
  const { model } = useSavedModel()
  if (!model) return null
  return (
    <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr)', gap: 12 }}>
      <Radio.Group
        optionType="button"
        buttonStyle="solid"
        value={kind}
        onChange={(e) => navigate({ search: { kind: e.target.value } })}
        options={[
          { value: 'pgsql', label: '新建 PostgreSQL 集群' },
          { value: 'redis', label: '新建 Redis 集群' },
        ]}
      />
      {kind === 'pgsql' ? <PgWizard key="pg" model={model} /> : <RedisWizard key="redis" model={model} />}
    </div>
  )
}

// ---------- 公共 ----------

function HostInput({ value, onChange, model }: { value: string; onChange: (v: string) => void; model: ConfigModel }) {
  const { specs } = useSpecsMap()
  const options = allHosts(model).map((ip) => {
    const groups = model.groups.filter((g) => ip in g.hosts).map((g) => g.name)
    const s = specs[ip]
    return {
      value: ip,
      label: (
        <Space size={4}>
          <span className="mono">{ip}</span>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            {groups.join(', ')}
            {s ? ` · ${s.cpu}C ${Math.round(s.memMb / 1024)}G` : ''}
          </Typography.Text>
        </Space>
      ),
    }
  })
  return (
    <AutoComplete
      value={value}
      onChange={onChange}
      options={options}
      style={{ width: 200 }}
      filterOption={(input, o) => String(o?.value).includes(input)}
    >
      <Input className="mono" placeholder="10.10.10.11" />
    </AutoComplete>
  )
}

/** 预览区：配置片段 + 涉及节点的内存预估 + 写入草稿 */
function Finish({
  name,
  group,
  errors,
  model,
  newModel,
  ips,
  hint,
}: {
  name: string
  group: Record<string, unknown>
  errors: string[]
  model: ConfigModel
  newModel: ConfigModel
  ips: string[]
  hint: string
}) {
  const config = useConfig()
  const schema = useSchema()
  const { specs, baseline } = useSpecsMap()
  const navigate = useNavigate()
  const { modal } = App.useApp()
  const known = new Set(allHosts(model))
  const snippet = useMemo(() => {
    try {
      // 用补丁函数在一个最小文档上生成片段，与写入工作台的格式完全一致
      return setIn('all:\n  children:\n    _: {}\n', ['all', 'children', name || 'new-cluster'], group).replace(/^all:\n {2}children:\n {4}_: \{\}\n/, '')
    } catch {
      return formatInline(group)
    }
  }, [name, group])

  const write = () => {
    const go = () => {
      editDraft(config.data!, (t) => setIn(t, ['all', 'children', name], group))
      navigate({ to: '/config', search: { scope: `group:${name}`, view: 'form' } })
    }
    if (hasDraft())
      modal.confirm({
        title: '工作台中已有未保存的修改',
        content: '新集群的配置会追加到现有草稿上，一起在工作台中核对、保存。',
        okText: '继续',
        onOk: go,
      })
    else go()
  }

  return (
    <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr)', gap: 12 }}>
      {errors.length > 0 && <Alert type="error" showIcon title="请先修正" description={<ul style={{ margin: 0, paddingInlineStart: 18 }}>{errors.map((e) => <li key={e}>{e}</li>)}</ul>} />}
      <Row gutter={12}>
        <Col span={12}>
          <Card size="small" title="将追加到 all.children 的配置片段">
            <pre className="mono" style={{ margin: 0, fontSize: 12, maxHeight: 420, overflow: 'auto' }}>
              {snippet}
            </pre>
          </Card>
        </Col>
        <Col span={12}>
          <Card size="small" title="涉及节点的内存预估">
            <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr)', gap: 12 }}>
              {ips.map((ip) => {
                const e = estimateNode(ip, { model: newModel, schema: schema.data, specs, baseline })
                return (
                  <div key={ip}>
                    <Space size={6} style={{ marginBottom: 4 }}>
                      <span className="mono">{ip}</span>
                      {!known.has(ip) && <Tag color="blue">新节点</Tag>}
                      <LevelTag level={e.level} />
                      {e.spec && (
                        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                          {e.spec.cpu}C / {Math.round(e.spec.memMb / 1024)}G（{e.spec.source === 'metrics' ? '监控' : '规划规格'}）
                        </Typography.Text>
                      )}
                    </Space>
                    <MemoryBar e={e} />
                    {e.warnings.map((w) => (
                      <div key={w}>
                        <Typography.Text type="warning" style={{ fontSize: 12 }}>
                          {w}
                        </Typography.Text>
                      </div>
                    ))}
                  </div>
                )
              })}
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                新节点没有监控数据，可在「节点与容量」中填写规划规格后再看预估。
              </Typography.Text>
            </div>
          </Card>
        </Col>
      </Row>
      <Alert type="info" showIcon title="下一步" description={hint} />
      <Space>
        <Button type="primary" disabled={errors.length > 0 || !config.data} onClick={write}>
          写入草稿，去工作台核对并保存
        </Button>
      </Space>
    </div>
  )
}

function withGroup(model: ConfigModel, name: string, group: { hosts: Record<string, Record<string, unknown>>; vars: Record<string, unknown> }, module: 'PGSQL' | 'REDIS'): ConfigModel {
  return { ...model, groups: [...model.groups.filter((g) => g.name !== name), { name, module, vars: group.vars, hosts: group.hosts }] }
}

// ---------- PostgreSQL ----------

const HA_SHAPES = [
  { value: 1, label: '单实例', desc: '1 主，无高可用，适合开发测试' },
  { value: 2, label: '一主一从', desc: '可自动故障切换，但单节点故障后不再有冗余' },
  { value: 3, label: '一主两从', desc: '推荐的生产形态，可容忍一台故障' },
]

function PgWizard({ model }: { model: ConfigModel }) {
  const [step, setStep] = useState(0)
  const [spec, setSpec] = useState<PgSpec>(() => ({
    name: 'pg-',
    version: '',
    conf: '',
    instances: [{ ip: '', role: 'primary' }],
    users: [{ name: 'dbuser_app', password: randomPassword(), role: 'dbrole_readwrite', pgbouncer: true, comment: '业务读写用户' }],
    databases: [{ name: 'app', owner: 'dbuser_app', comment: '' }],
    vip: '',
    vipInterface: 'eth0',
  }))
  const set = (patch: Partial<PgSpec>) => setSpec((s) => ({ ...s, ...patch }))
  const groups = model.groups.map((g) => g.name)
  const errors = validatePg(spec, groups)
  const group = pgGroup(spec)
  const newModel = withGroup(model, spec.name, group as never, 'PGSQL')

  const setShape = (n: number) => {
    const ins = spec.instances.slice(0, n)
    while (ins.length < n) ins.push({ ip: '', role: 'replica' })
    ins[0] = { ...ins[0], role: 'primary' }
    for (let i = 1; i < ins.length; i++) if (ins[i].role === 'primary') ins[i] = { ...ins[i], role: 'replica' }
    set({ instances: ins })
  }

  const globalVersion = String(model.globalVars.pg_version ?? '18')
  const globalConf = String(model.globalVars.pg_conf ?? 'oltp.yml')

  return (
    <Card size="small">
      <Steps
        size="small"
        current={step}
        onChange={setStep}
        style={{ marginBottom: 16 }}
        items={[{ title: '基本信息与形态' }, { title: '节点' }, { title: '用户与数据库' }, { title: '预览' }]}
      />
      {step === 0 && (
        <Form layout="vertical" style={{ maxWidth: 640 }}>
          <Form.Item label="集群名称（pg_cluster）" required help="同时作为 inventory 组名，例如 pg-test、pg-order">
            <Input value={spec.name} onChange={(e) => set({ name: e.target.value.trim() })} className="mono" />
          </Form.Item>
          <Form.Item label="高可用形态">
            <Radio.Group value={Math.min(spec.instances.length, 3)} onChange={(e) => setShape(e.target.value)}>
              <Space direction="vertical">
                {HA_SHAPES.map((s) => (
                  <Radio key={s.value} value={s.value}>
                    {s.label}
                    <Typography.Text type="secondary" style={{ marginInlineStart: 8, fontSize: 12 }}>
                      {s.desc}
                    </Typography.Text>
                  </Radio>
                ))}
              </Space>
            </Radio.Group>
          </Form.Item>
          <Row gutter={12}>
            <Col span={12}>
              <Form.Item label="PostgreSQL 大版本" help={`留空使用全局值 ${globalVersion}`}>
                <Select allowClear value={spec.version || undefined} onChange={(v) => set({ version: v ?? '' })} options={['18', '17', '16', '15', '14', '13'].map((v) => ({ value: v, label: v }))} />
              </Form.Item>
            </Col>
            <Col span={12}>
              <Form.Item label="调优模板（pg_conf）" help={`留空使用全局值 ${globalConf}`}>
                <Select
                  allowClear
                  value={spec.conf || undefined}
                  onChange={(v) => set({ conf: v ?? '' })}
                  options={[
                    { value: 'oltp.yml', label: 'oltp — 在线事务（默认）' },
                    { value: 'olap.yml', label: 'olap — 分析型' },
                    { value: 'crit.yml', label: 'crit — 核心金融，强一致' },
                    { value: 'tiny.yml', label: 'tiny — 微型实例 / 开发' },
                  ]}
                />
              </Form.Item>
            </Col>
          </Row>
        </Form>
      )}
      {step === 1 && (
        <div style={{ display: 'grid', gap: 12, maxWidth: 820 }}>
          <Table
            size="small"
            pagination={false}
            rowKey={(_, i) => String(i)}
            dataSource={spec.instances}
            columns={[
              { title: '序号', width: 60, render: (_, __, i) => `${spec.name}-${i + 1}` },
              {
                title: 'IP',
                render: (_, ins, i) => (
                  <HostInput
                    model={model}
                    value={ins.ip}
                    onChange={(ip) => set({ instances: spec.instances.map((x, j) => (j === i ? { ...x, ip: ip.trim() } : x)) })}
                  />
                ),
              },
              {
                title: '角色',
                width: 280,
                render: (_, ins, i) => (
                  <Radio.Group
                    size="small"
                    value={ins.role}
                    onChange={(e) =>
                      set({
                        instances: spec.instances.map((x, j) =>
                          j === i ? { ...x, role: e.target.value } : e.target.value === 'primary' && x.role === 'primary' ? { ...x, role: 'replica' } : x,
                        ),
                      })
                    }
                    options={[
                      { value: 'primary', label: '主库' },
                      { value: 'replica', label: '从库' },
                      { value: 'offline', label: '离线从库' },
                    ]}
                    optionType="button"
                  />
                ),
              },
              {
                width: 50,
                render: (_, __, i) =>
                  spec.instances.length > 1 && (
                    <Button type="text" danger icon={<DeleteOutlined />} onClick={() => set({ instances: spec.instances.filter((_, j) => j !== i) })} />
                  ),
              },
            ]}
          />
          <Space>
            <Button icon={<PlusOutlined />} onClick={() => set({ instances: [...spec.instances, { ip: '', role: 'replica' }] })}>
              添加实例
            </Button>
          </Space>
          <Divider style={{ margin: '4px 0' }} />
          <Row gutter={12}>
            <Col span={10}>
              <Form.Item label="L2 VIP（可选）" help="留空不启用；需要所有成员在同一二层网络">
                <Input className="mono" placeholder="10.10.10.3/24" value={spec.vip} onChange={(e) => set({ vip: e.target.value.trim() })} />
              </Form.Item>
            </Col>
            {spec.vip && (
              <Col span={6}>
                <Form.Item label="网卡">
                  <Input className="mono" value={spec.vipInterface} onChange={(e) => set({ vipInterface: e.target.value.trim() })} />
                </Form.Item>
              </Col>
            )}
          </Row>
        </div>
      )}
      {step === 2 && (
        <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr)', gap: 16 }}>
          <Card size="small" type="inner" title="业务用户（pg_users）">
            <Table
              size="small"
              pagination={false}
              rowKey={(_, i) => String(i)}
              dataSource={spec.users}
              columns={[
                { title: '用户名', render: (_, u, i) => <Input className="mono" value={u.name} onChange={(e) => set({ users: spec.users.map((x, j) => (j === i ? { ...x, name: e.target.value.trim() } : x)) })} /> },
                {
                  title: '密码',
                  render: (_, u, i) => (
                    <Space.Compact style={{ width: '100%' }}>
                      <Input.Password className="mono" value={u.password} onChange={(e) => set({ users: spec.users.map((x, j) => (j === i ? { ...x, password: e.target.value } : x)) })} />
                      <Button icon={<ReloadOutlined />} onClick={() => set({ users: spec.users.map((x, j) => (j === i ? { ...x, password: randomPassword() } : x)) })} />
                    </Space.Compact>
                  ),
                },
                {
                  title: '权限',
                  width: 170,
                  render: (_, u, i) => (
                    <Select
                      value={u.role}
                      style={{ width: '100%' }}
                      onChange={(v) => set({ users: spec.users.map((x, j) => (j === i ? { ...x, role: v } : x)) })}
                      options={[
                        { value: 'dbrole_readwrite', label: '读写' },
                        { value: 'dbrole_readonly', label: '只读' },
                        { value: 'dbrole_admin', label: '管理（可 DDL）' },
                        { value: 'dbrole_offline', label: '离线只读' },
                      ]}
                    />
                  ),
                },
                { title: '连接池', width: 70, render: (_, u, i) => <Switch size="small" checked={u.pgbouncer} onChange={(v) => set({ users: spec.users.map((x, j) => (j === i ? { ...x, pgbouncer: v } : x)) })} /> },
                { title: '备注', render: (_, u, i) => <Input value={u.comment} onChange={(e) => set({ users: spec.users.map((x, j) => (j === i ? { ...x, comment: e.target.value } : x)) })} /> },
                { width: 40, render: (_, __, i) => <Button type="text" danger icon={<DeleteOutlined />} onClick={() => set({ users: spec.users.filter((_, j) => j !== i) })} /> },
              ]}
            />
            <Button
              icon={<PlusOutlined />}
              style={{ marginTop: 8 }}
              onClick={() => set({ users: [...spec.users, { name: '', password: randomPassword(), role: 'dbrole_readonly', pgbouncer: true, comment: '' }] })}
            >
              添加用户
            </Button>
          </Card>
          <Card size="small" type="inner" title="业务数据库（pg_databases）">
            <Table
              size="small"
              pagination={false}
              rowKey={(_, i) => String(i)}
              dataSource={spec.databases}
              columns={[
                { title: '库名', render: (_, d, i) => <Input className="mono" value={d.name} onChange={(e) => set({ databases: spec.databases.map((x, j) => (j === i ? { ...x, name: e.target.value.trim() } : x)) })} /> },
                {
                  title: '属主',
                  render: (_, d, i) => (
                    <Select
                      allowClear
                      style={{ width: '100%' }}
                      value={d.owner || undefined}
                      onChange={(v) => set({ databases: spec.databases.map((x, j) => (j === i ? { ...x, owner: v ?? '' } : x)) })}
                      options={spec.users.filter((u) => u.name).map((u) => ({ value: u.name, label: u.name }))}
                    />
                  ),
                },
                { title: '备注', render: (_, d, i) => <Input value={d.comment} onChange={(e) => set({ databases: spec.databases.map((x, j) => (j === i ? { ...x, comment: e.target.value } : x)) })} /> },
                { width: 40, render: (_, __, i) => <Button type="text" danger icon={<DeleteOutlined />} onClick={() => set({ databases: spec.databases.filter((_, j) => j !== i) })} /> },
              ]}
            />
            <Button icon={<PlusOutlined />} style={{ marginTop: 8 }} onClick={() => set({ databases: [...spec.databases, { name: '', owner: '', comment: '' }] })}>
              添加数据库
            </Button>
          </Card>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            扩展、HBA、服务、备份仓库等可以在工作台里继续配置（复杂对象编辑器）。
          </Typography.Text>
        </div>
      )}
      {step === 3 && (
        <Finish
          name={spec.name}
          group={group}
          errors={errors}
          model={model}
          newModel={newModel}
          ips={spec.instances.map((i) => i.ip).filter(Boolean)}
          hint="保存配置后，到集群页的「待应用变更」中依次执行：纳管新节点（node-add）→ 初始化集群（pgsql-create）。"
        />
      )}
      {step < 3 && (
        <Space style={{ marginTop: 16 }}>
          {step > 0 && <Button onClick={() => setStep(step - 1)}>上一步</Button>}
          <Button type="primary" onClick={() => setStep(step + 1)}>
            下一步
          </Button>
        </Space>
      )}
    </Card>
  )
}

// ---------- Redis ----------

const REDIS_MODES: { value: RedisMode; label: string; desc: string }[] = [
  { value: 'standalone', label: '主从（standalone）', desc: '一个或多个独立实例，可用 replica_of 组成主从' },
  { value: 'sentinel', label: '哨兵（sentinel）', desc: '哨兵实例，监控其他主从集群并自动故障切换' },
  { value: 'cluster', label: '原生集群（cluster）', desc: 'Redis Cluster 分片，至少 3 主' },
]

function RedisWizard({ model }: { model: ConfigModel }) {
  const [step, setStep] = useState(0)
  const [spec, setSpec] = useState<RedisSpec>(() => ({
    name: 'redis-',
    mode: 'standalone',
    maxMemory: '1GB',
    password: randomPassword(16),
    nodes: [{ ip: '', ports: [6379, 6380], replicaOf: {} }],
    clusterReplicas: 1,
    monitors: [],
  }))
  const set = (patch: Partial<RedisSpec>) => setSpec((s) => ({ ...s, ...patch }))
  const setNode = (i: number, patch: Partial<RedisSpec['nodes'][number]>) => set({ nodes: spec.nodes.map((n, j) => (j === i ? { ...n, ...patch } : n)) })
  const groups = model.groups.map((g) => g.name)
  const errors = validateRedis(spec, groups)
  const group = redisGroup(spec)
  const newModel = withGroup(model, spec.name, group as never, 'REDIS')

  const setMode = (mode: RedisMode) => {
    const defaults: Record<RedisMode, number[]> = { standalone: [6379, 6380], sentinel: [26379, 26380, 26381], cluster: [6379, 6380, 6381] }
    set({ mode, nodes: spec.nodes.map((n) => ({ ...n, ports: defaults[mode], replicaOf: {} })), maxMemory: mode === 'sentinel' ? '16MB' : spec.maxMemory === '16MB' ? '1GB' : spec.maxMemory })
  }

  // 本集群所有实例，作为主从模式的上游候选
  const upstreams = spec.nodes.flatMap((n) => n.ports.map((p) => `${n.ip} ${p}`)).filter((s) => !s.startsWith(' '))

  // 哨兵可监控的现有 standalone 主实例
  const monitorCandidates = useMemo(() => {
    const out: SentinelMonitor[] = []
    for (const g of model.groups.filter((x) => x.module === 'REDIS' && (x.vars.redis_mode ?? 'standalone') === 'standalone')) {
      for (const [ip, hv] of Object.entries(g.hosts)) {
        const inst = (hv.redis_instances ?? {}) as Record<string, Record<string, unknown>>
        for (const [port, iv] of Object.entries(inst))
          if (!iv?.replica_of) out.push({ name: g.name, host: ip, port: Number(port), password: String(g.vars.redis_password ?? ''), quorum: 2 })
      }
    }
    return out
  }, [model])

  return (
    <Card size="small">
      <Steps size="small" current={step} onChange={setStep} style={{ marginBottom: 16 }} items={[{ title: '模式与基本信息' }, { title: '节点与实例' }, { title: '预览' }]} />
      {step === 0 && (
        <Form layout="vertical" style={{ maxWidth: 640 }}>
          <Form.Item label="集群名称（redis_cluster）" required help="同时作为 inventory 组名，例如 redis-cache">
            <Input className="mono" value={spec.name} onChange={(e) => set({ name: e.target.value.trim() })} />
          </Form.Item>
          <Form.Item label="部署模式（redis_mode）">
            <Radio.Group value={spec.mode} onChange={(e) => setMode(e.target.value)}>
              <Space direction="vertical">
                {REDIS_MODES.map((m) => (
                  <Radio key={m.value} value={m.value}>
                    {m.label}
                    <Typography.Text type="secondary" style={{ marginInlineStart: 8, fontSize: 12 }}>
                      {m.desc}
                    </Typography.Text>
                  </Radio>
                ))}
              </Space>
            </Radio.Group>
          </Form.Item>
          <Row gutter={12}>
            <Col span={12}>
              <Form.Item label="每实例最大内存（redis_max_memory）">
                <Input className="mono" value={spec.maxMemory} onChange={(e) => set({ maxMemory: e.target.value.trim() })} />
              </Form.Item>
            </Col>
            <Col span={12}>
              <Form.Item label="密码（redis_password）" help="留空不启用认证">
                <Space.Compact style={{ width: '100%' }}>
                  <Input.Password className="mono" value={spec.password} onChange={(e) => set({ password: e.target.value })} />
                  <Button icon={<ReloadOutlined />} onClick={() => set({ password: randomPassword(16) })} />
                </Space.Compact>
              </Form.Item>
            </Col>
          </Row>
          {spec.mode === 'cluster' && (
            <Form.Item label="每个主节点的从节点数（redis_cluster_replicas）">
              <InputNumber min={0} max={3} value={spec.clusterReplicas} onChange={(v) => set({ clusterReplicas: v ?? 1 })} />
            </Form.Item>
          )}
        </Form>
      )}
      {step === 1 && (
        <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr)', gap: 12 }}>
          {spec.nodes.map((n, i) => (
            <Card
              key={i}
              size="small"
              type="inner"
              title={`节点 ${i + 1}（redis_node: ${i + 1}）`}
              extra={spec.nodes.length > 1 && <Button type="text" danger icon={<DeleteOutlined />} onClick={() => set({ nodes: spec.nodes.filter((_, j) => j !== i) })} />}
            >
              <Space align="start" wrap>
                <Form.Item label="IP" style={{ marginBottom: 0 }}>
                  <HostInput model={model} value={n.ip} onChange={(ip) => setNode(i, { ip: ip.trim() })} />
                </Form.Item>
                <Form.Item label="实例端口" style={{ marginBottom: 0 }}>
                  <Select
                    mode="tags"
                    style={{ minWidth: 260 }}
                    value={n.ports.map(String)}
                    onChange={(v: string[]) => setNode(i, { ports: [...new Set(v.map(Number).filter((x) => x > 0))].sort((a, b) => a - b) })}
                  />
                </Form.Item>
              </Space>
              {spec.mode === 'standalone' && n.ports.length > 0 && (
                <Table
                  size="small"
                  pagination={false}
                  style={{ marginTop: 8, maxWidth: 520 }}
                  rowKey={(p) => String(p)}
                  dataSource={n.ports}
                  columns={[
                    { title: '端口', width: 80, render: (p) => <span className="mono">{p}</span> },
                    {
                      title: '复制自（replica_of）',
                      render: (p: number) => (
                        <Select
                          allowClear
                          placeholder="主实例"
                          style={{ width: '100%' }}
                          value={n.replicaOf[p] || undefined}
                          onChange={(v) => setNode(i, { replicaOf: { ...n.replicaOf, [p]: v ?? '' } })}
                          options={upstreams.filter((u) => u !== `${n.ip} ${p}`).map((u) => ({ value: u, label: <span className="mono">{u}</span> }))}
                        />
                      ),
                    },
                  ]}
                />
              )}
            </Card>
          ))}
          <Space>
            <Button
              icon={<PlusOutlined />}
              onClick={() => set({ nodes: [...spec.nodes, { ip: '', ports: spec.nodes[spec.nodes.length - 1]?.ports ?? [6379], replicaOf: {} }] })}
            >
              添加节点
            </Button>
          </Space>
          {spec.mode === 'sentinel' && (
            <Card size="small" type="inner" title="监控的主从集群（redis_sentinel_monitor）">
              <Table
                size="small"
                pagination={false}
                rowKey={(_, i) => String(i)}
                dataSource={spec.monitors}
                columns={[
                  { title: '名称', render: (_, m, i) => <Input value={m.name} onChange={(e) => set({ monitors: spec.monitors.map((x, j) => (j === i ? { ...x, name: e.target.value.trim() } : x)) })} /> },
                  { title: '主实例 IP', render: (_, m, i) => <Input className="mono" value={m.host} onChange={(e) => set({ monitors: spec.monitors.map((x, j) => (j === i ? { ...x, host: e.target.value.trim() } : x)) })} /> },
                  { title: '端口', width: 100, render: (_, m, i) => <InputNumber value={m.port} onChange={(v) => set({ monitors: spec.monitors.map((x, j) => (j === i ? { ...x, port: v ?? 6379 } : x)) })} /> },
                  { title: '密码', render: (_, m, i) => <Input.Password value={m.password} onChange={(e) => set({ monitors: spec.monitors.map((x, j) => (j === i ? { ...x, password: e.target.value } : x)) })} /> },
                  { title: 'quorum', width: 90, render: (_, m, i) => <InputNumber min={1} value={m.quorum} onChange={(v) => set({ monitors: spec.monitors.map((x, j) => (j === i ? { ...x, quorum: v ?? 2 } : x)) })} /> },
                  { width: 40, render: (_, __, i) => <Button type="text" danger icon={<DeleteOutlined />} onClick={() => set({ monitors: spec.monitors.filter((_, j) => j !== i) })} /> },
                ]}
              />
              <Space style={{ marginTop: 8 }} wrap>
                <Button icon={<PlusOutlined />} onClick={() => set({ monitors: [...spec.monitors, { name: '', host: '', port: 6379, password: '', quorum: 2 }] })}>
                  手动添加
                </Button>
                {monitorCandidates
                  .filter((c) => !spec.monitors.some((m) => m.host === c.host && m.port === c.port))
                  .map((c) => (
                    <Button key={`${c.host}:${c.port}`} size="small" onClick={() => set({ monitors: [...spec.monitors, c] })}>
                      + {c.name}（{c.host}:{c.port}）
                    </Button>
                  ))}
              </Space>
            </Card>
          )}
        </div>
      )}
      {step === 2 && (
        <Finish
          name={spec.name}
          group={group}
          errors={errors}
          model={model}
          newModel={newModel}
          ips={spec.nodes.map((n) => n.ip).filter(Boolean).sort(compareIp)}
          hint="保存配置后，到集群页的「待应用变更」中依次执行：纳管新节点（node-add）→ 部署 Redis（redis.yml）。"
        />
      )}
      {step < 2 && (
        <Space style={{ marginTop: 16 }}>
          {step > 0 && <Button onClick={() => setStep(step - 1)}>上一步</Button>}
          <Button type="primary" onClick={() => setStep(step + 1)}>
            下一步
          </Button>
        </Space>
      )}
    </Card>
  )
}
