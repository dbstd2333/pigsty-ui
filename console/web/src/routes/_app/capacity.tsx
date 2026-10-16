import { useMemo, useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Alert, App, Button, Card, Col, Form, InputNumber, Row, Space, Statistic, Switch, Table, Tag, Tooltip, Typography } from 'antd'
import { EditOutlined, ExperimentOutlined, SyncOutlined } from '@ant-design/icons'
import { api } from '@/api/endpoints'
import { qk, useSchema, useSettings } from '@/api/queries'
import { useSavedModel } from '@/features/config/useSavedModel'
import { BASELINE, type BaselineId } from '@/features/memory/baseline'
import { DEFAULT_ASSUMPTIONS, estimateAll, type Assumptions, type Component, type NodeEstimate, type NodeSpecInput } from '@/features/memory/estimate'
import { fmtMb, LAYER_META, LevelTag, MemoryBar } from '@/features/memory/MemoryBar'
import { SpecDialog } from '@/features/memory/SpecDialog'
import { useSpecsMap } from '@/features/memory/useSpecs'
import { compareIp } from '@/features/config/model'

export const Route = createFileRoute('/_app/capacity')({
  component: CapacityPage,
})

const LEVEL_ORDER = { danger: 0, warn: 1, unknown: 2, ok: 3 }

function CapacityPage() {
  const { model } = useSavedModel()
  const schema = useSchema()
  const { specs, baseline, nodeSpecs } = useSpecsMap()
  const [a, setA] = useState<Assumptions>(DEFAULT_ASSUMPTIONS)
  const [assumedMem, setAssumedMem] = useState<number | null>(null)
  const [editing, setEditing] = useState<string>()

  // 规格未知的节点可以按「假设内存」试算
  const effSpecs = useMemo(() => {
    if (!assumedMem || !model) return specs
    const out: Record<string, NodeSpecInput> = { ...specs }
    for (const g of model.groups) for (const ip of Object.keys(g.hosts)) out[ip] ??= { memMb: assumedMem * 1024, cpu: 4, source: 'assumed' }
    return out
  }, [specs, assumedMem, model])

  const rows = useMemo(
    () =>
      model
        ? estimateAll({ model, schema: schema.data, specs: effSpecs, assumptions: a, baseline }).sort(
            (x, y) => LEVEL_ORDER[x.level] - LEVEL_ORDER[y.level] || compareIp(x.ip, y.ip),
          )
        : [],
    [model, schema.data, effSpecs, a, baseline],
  )

  const count = (l: NodeEstimate['level']) => rows.filter((r) => r.level === l).length

  return (
    <div style={{ display: 'grid', gap: 12 }}>
      <Row gutter={12}>
        <Col span={6}>
          <Card size="small">
            <Statistic title="节点" value={rows.length} />
          </Card>
        </Col>
        <Col span={6}>
          <Card size="small">
            <Statistic title="超配风险" value={count('danger')} valueStyle={{ color: count('danger') ? '#cf1322' : undefined }} />
          </Card>
        </Col>
        <Col span={6}>
          <Card size="small">
            <Statistic title="偏高" value={count('warn')} valueStyle={{ color: count('warn') ? '#d48806' : undefined }} />
          </Card>
        </Col>
        <Col span={6}>
          <Card size="small">
            <Statistic title="规格未知" value={count('unknown')} />
          </Card>
        </Col>
      </Row>

      <Card size="small" title="预估假设" extra={<Typography.Text type="secondary" style={{ fontSize: 12 }}>只影响本页计算，不会保存</Typography.Text>}>
        <Form layout="inline" size="small">
          <Form.Item label="活跃连接数" tooltip="留空：min(max_connections, 4 × CPU 核数)">
            <InputNumber min={1} placeholder="自动" value={a.activeConn} onChange={(v) => setA({ ...a, activeConn: v })} />
          </Form.Item>
          <Form.Item label="单连接开销 MB">
            <InputNumber min={1} value={a.perConnMb} onChange={(v) => setA({ ...a, perConnMb: v ?? 10 })} />
          </Form.Item>
          <Form.Item label="Redis 系数" tooltip="内存碎片 + BGSAVE 写时复制">
            <InputNumber min={1} max={3} step={0.1} value={a.redisFactor} onChange={(v) => setA({ ...a, redisFactor: v ?? 1.3 })} />
          </Form.Item>
          <Form.Item label="系统预留 %">
            <InputNumber min={0} max={50} value={a.reservePct * 100} onChange={(v) => setA({ ...a, reservePct: (v ?? 5) / 100 })} />
          </Form.Item>
          <Form.Item label="未知节点按" tooltip="对没有指标、也没有规划规格的节点，按这个内存试算，便于选型">
            <Space>
              <Switch size="small" checked={assumedMem !== null} onChange={(v) => setAssumedMem(v ? 16 : null)} />
              {assumedMem !== null && <InputNumber min={1} value={assumedMem} onChange={(v) => setAssumedMem(v ?? 16)} addonAfter="GB" style={{ width: 120 }} />}
            </Space>
          </Form.Item>
          <Button size="small" onClick={() => setA(DEFAULT_ASSUMPTIONS)}>
            恢复默认
          </Button>
        </Form>
      </Card>

      <Card size="small" title="节点内存预估">
        <Table<NodeEstimate>
          size="small"
          rowKey="ip"
          pagination={false}
          dataSource={rows}
          expandable={{ expandedRowRender: (e) => <Breakdown e={e} />, expandRowByClick: false }}
          columns={[
            {
              title: '节点',
              render: (_, e) => (
                <Space direction="vertical" size={0}>
                  <span className="mono">{e.ip}</span>
                  <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                    {e.groups.join(', ')}
                  </Typography.Text>
                </Space>
              ),
            },
            {
              title: '规格',
              width: 150,
              render: (_, e) =>
                e.spec ? (
                  <Space size={4}>
                    {e.spec.cpu}C / {fmtMb(e.spec.memMb)}
                    <Tag style={{ fontSize: 10, lineHeight: '16px' }}>{e.spec.source === 'metrics' ? '监控' : e.spec.source === 'spec' ? '规划' : '假设'}</Tag>
                  </Space>
                ) : (
                  <Button size="small" type="link" icon={<EditOutlined />} onClick={() => setEditing(e.ip)} style={{ padding: 0 }}>
                    填写规划规格
                  </Button>
                ),
            },
            { title: '状态', width: 100, render: (_, e) => <LevelTag level={e.level} /> },
            { title: '占用（按层，典型值）', render: (_, e) => <MemoryBar e={e} /> },
            {
              title: '提示',
              width: 260,
              render: (_, e) =>
                e.warnings.length > 0 && (
                  <Tooltip title={<ul style={{ margin: 0, paddingInlineStart: 16 }}>{e.warnings.map((w) => <li key={w}>{w}</li>)}</ul>}>
                    <Typography.Text type="warning" ellipsis style={{ maxWidth: 240 }}>
                      {e.warnings[0]}
                      {e.warnings.length > 1 && ` 等 ${e.warnings.length} 条`}
                    </Typography.Text>
                  </Tooltip>
                ),
            },
          ]}
        />
        <Space size={12} style={{ marginTop: 8 }} wrap>
          {Object.entries(LAYER_META).map(([k, m]) => (
            <Space key={k} size={4}>
              <span style={{ display: 'inline-block', width: 10, height: 10, background: m.color, borderRadius: 2 }} />
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {m.label}
              </Typography.Text>
            </Space>
          ))}
        </Space>
      </Card>
      {editing && <SpecDialog ip={editing} spec={nodeSpecs.data?.find((s) => s.ip === editing)} onClose={() => setEditing(undefined)} />}
    </div>
  )
}

/** 展开行：组件明细 + 公式 + 实测对比与经验值校准 */
function Breakdown({ e }: { e: NodeEstimate }) {
  const [measure, setMeasure] = useState(false)
  const settings = useSettings()
  const queryClient = useQueryClient()
  const { message } = App.useApp()
  const mem = useQuery({
    queryKey: qk.nodeMemory(e.ip),
    queryFn: () => api.nodeMemory(e.ip),
    enabled: measure,
  })
  const refresh = useMutation({
    mutationFn: () => api.nodeMemory(e.ip, true),
    onSuccess: (d) => queryClient.setQueryData(qk.nodeMemory(e.ip), d),
  })
  const calibrate = useMutation({
    mutationFn: (patch: Record<string, number>) => api.saveSettings({ memoryBaseline: { ...settings.data?.memoryBaseline, ...patch } }),
    onSuccess: (s) => {
      queryClient.setQueryData(qk.settings, s)
      message.success('已用实测值更新经验值')
    },
    onError: (err) => message.error(`保存失败：${(err as Error).message}`),
  })

  const actual = (c: Component) => {
    if (!c.service || !mem.data) return undefined
    const hits = mem.data.services.filter((s) => s.name === c.service || s.name.startsWith(c.service + '@') || (c.service === 'redis' && s.name.startsWith('redis')))
    const bytes = hits.reduce((n, s) => n + (s.bytes ?? 0), 0)
    return hits.length ? bytes / 1024 / 1024 : undefined
  }

  const rows = e.components
  const calibratable = rows
    .filter((c) => c.id in BASELINE)
    .map((c) => ({ c, act: actual(c) }))
    .filter((x) => x.act !== undefined && Math.abs(x.act - x.c.typical) / Math.max(x.c.typical, 1) > 0.3)

  return (
    <div style={{ display: 'grid', gap: 8 }}>
      <Space>
        <Button size="small" icon={<ExperimentOutlined />} onClick={() => setMeasure(true)} loading={mem.isFetching} disabled={measure && !!mem.data}>
          采集实测值
        </Button>
        {mem.data && (
          <Button size="small" icon={<SyncOutlined />} onClick={() => refresh.mutate()} loading={refresh.isPending}>
            重新采集
          </Button>
        )}
        {mem.data && !mem.data.source.ok && <Typography.Text type="danger">采集失败：{mem.data.source.error}</Typography.Text>}
        {calibratable.length > 0 && (
          <Button
            size="small"
            type="primary"
            ghost
            loading={calibrate.isPending}
            onClick={() => calibrate.mutate(Object.fromEntries(calibratable.map((x) => [x.c.id as BaselineId, Math.round(x.act!)])))}
          >
            用实测值校准 {calibratable.length} 项经验值
          </Button>
        )}
      </Space>
      {e.pg && (
        <Alert
          type="info"
          showIcon
          title={`PG ${e.pg.cluster}（${e.pg.role}）：模板 ${e.pg.conf}，max_connections ${e.pg.maxConnections}，shared_buffers ${e.pg.sharedBuffers} MB，work_mem ${e.pg.workMem} MB，maintenance_work_mem ${e.pg.maintenanceWorkMem} MB`}
        />
      )}
      <Table<Component>
        size="small"
        rowKey="id"
        pagination={false}
        dataSource={rows}
        columns={[
          {
            title: '组件',
            width: 200,
            render: (_, c) => (
              <Space size={4}>
                <span style={{ display: 'inline-block', width: 8, height: 8, background: LAYER_META[c.layer].color, borderRadius: 2 }} />
                {c.label}
              </Space>
            ),
          },
          { title: '常驻', width: 90, align: 'right', render: (_, c) => fmtMb(c.resident) },
          { title: '典型', width: 90, align: 'right', render: (_, c) => fmtMb(c.typical) },
          { title: '上界', width: 90, align: 'right', render: (_, c) => fmtMb(c.peak) },
          {
            title: '实测',
            width: 110,
            align: 'right',
            render: (_, c) => {
              const v = actual(c)
              if (v === undefined) return <Typography.Text type="secondary">-</Typography.Text>
              const off = Math.abs(v - c.typical) / Math.max(c.typical, 1)
              return <Typography.Text type={off > 0.3 ? 'warning' : undefined}>{fmtMb(v)}</Typography.Text>
            },
          },
          {
            title: '计算过程',
            render: (_, c) => (
              <div style={{ fontSize: 12, color: '#666' }}>
                {c.detail.map((d) => (
                  <div key={d}>{d}</div>
                ))}
              </div>
            ),
          },
        ]}
        summary={() => (
          <Table.Summary.Row>
            <Table.Summary.Cell index={0}>
              <b>合计</b>
            </Table.Summary.Cell>
            <Table.Summary.Cell index={1} align="right">
              <b>{fmtMb(e.resident)}</b>
            </Table.Summary.Cell>
            <Table.Summary.Cell index={2} align="right">
              <b>{fmtMb(e.typical)}</b>
            </Table.Summary.Cell>
            <Table.Summary.Cell index={3} align="right">
              <b>{fmtMb(e.peak)}</b>
            </Table.Summary.Cell>
            <Table.Summary.Cell index={4} />
            <Table.Summary.Cell index={5}>
              {e.spec && (
                <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                  节点内存 {fmtMb(e.spec.memMb)}；判定：典型 &gt; 90% 或常驻 &gt; 75% 为红，典型 &gt; 75% 为黄{e.hugeMb ? `；大页 ${fmtMb(e.hugeMb)}` : ''}
                </Typography.Text>
              )}
            </Table.Summary.Cell>
          </Table.Summary.Row>
        )}
      />
    </div>
  )
}
