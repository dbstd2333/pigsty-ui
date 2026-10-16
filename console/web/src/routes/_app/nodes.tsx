import { useMemo, useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { Alert, Badge, Button, Card, Space, Table, Tag, Tooltip, Typography } from 'antd'
import { EditOutlined, LineChartOutlined } from '@ant-design/icons'
import { useAlerts } from '@/api/queries'
import type { NodeSpec, NodeStatus } from '@/api/types'
import { ActionButton } from '@/features/actions/RunAction'
import { allHosts, compareIp, MODULE_COLORS } from '@/features/config/model'
import { useSavedModel } from '@/features/config/useSavedModel'
import { UsageProgress } from '@/features/memory/MemoryBar'
import { SpecDialog } from '@/features/memory/SpecDialog'
import { useSpecsMap } from '@/features/memory/useSpecs'
import { useGrafana } from '@/features/status/grafana'

export const Route = createFileRoute('/_app/nodes')({
  component: NodesPage,
})

interface Row {
  ip: string
  groups: { name: string; module: string | null }[]
  st?: NodeStatus
  spec?: NodeSpec
}

const GB = 1024 ** 3

function NodesPage() {
  const { model } = useSavedModel()
  const { nodes, nodeSpecs } = useSpecsMap()
  const alerts = useAlerts()
  const grafana = useGrafana()
  const [editing, setEditing] = useState<string>()

  const rows = useMemo<Row[]>(() => {
    if (!model) return []
    const st = new Map((nodes.data?.items ?? []).map((n) => [n.ip, n]))
    const sp = new Map((nodeSpecs.data ?? []).map((n) => [n.ip, n]))
    return allHosts(model).map((ip) => ({
      ip,
      groups: model.groups.filter((g) => ip in g.hosts).map((g) => ({ name: g.name, module: g.module })),
      st: st.get(ip),
      spec: sp.get(ip),
    }))
  }, [model, nodes.data, nodeSpecs.data])

  const unmanaged = rows.filter((r) => !r.st?.managed).map((r) => r.ip)
  const nodeAlerts = (ip: string) => (alerts.data?.items ?? []).filter((a) => a.labels.ip === ip || a.labels.instance?.startsWith(ip))

  return (
    <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr)', gap: 12 }}>
      {nodes.data && !nodes.data.source.ok && (
        <Alert type="warning" showIcon title={`无法从 VictoriaMetrics 获取节点指标：${nodes.data.source.error ?? ''}`} description="可在「设置」中配置 VictoriaMetrics 地址。没有指标时，容量预估使用规划规格。" />
      )}
      <Card
        size="small"
        title={`节点（${rows.length}）`}
        extra={
          <Space>
            {unmanaged.length > 0 && (
              <ActionButton action="node-add" params={{ ips: unmanaged }} lockParams={false}>
                纳管未纳管节点（{unmanaged.length}）
              </ActionButton>
            )}
            <Link to="/capacity">
              <Button>容量预估</Button>
            </Link>
          </Space>
        }
      >
        <Table<Row>
          size="small"
          rowKey="ip"
          loading={nodes.isLoading}
          pagination={false}
          dataSource={rows}
          columns={[
            {
              title: '节点',
              sorter: (a, b) => compareIp(a.ip, b.ip),
              render: (_, r) => (
                <Space direction="vertical" size={0}>
                  <Space size={4}>
                    <span className="mono">{r.ip}</span>
                    {nodeAlerts(r.ip).length > 0 && (
                      <Tooltip title={nodeAlerts(r.ip).map((a) => a.summary || a.name).join('；')}>
                        <Tag color="red">{nodeAlerts(r.ip).length} 告警</Tag>
                      </Tooltip>
                    )}
                  </Space>
                  <Space size={2} wrap>
                    {r.groups.map((g) => (
                      <Tag key={g.name} color={g.module ? MODULE_COLORS[g.module as keyof typeof MODULE_COLORS] : undefined} style={{ fontSize: 11, lineHeight: '16px' }}>
                        {g.name}
                      </Tag>
                    ))}
                  </Space>
                </Space>
              ),
            },
            {
              title: '状态',
              width: 100,
              render: (_, r) =>
                !r.st?.managed ? <Badge status="default" text="未纳管" /> : r.st.up ? <Badge status="success" text="在线" /> : r.st.up === false ? <Badge status="error" text="离线" /> : <Badge status="default" text="未知" />,
            },
            {
              title: '规格',
              width: 170,
              render: (_, r) =>
                r.st?.memTotalBytes ? (
                  <span>
                    {r.st.cpuCores ?? '?'}C / {(r.st.memTotalBytes / GB).toFixed(0)}G
                    {r.st.diskTotalBytes ? ` / ${(r.st.diskTotalBytes / GB).toFixed(0)}G` : ''}
                  </span>
                ) : r.spec ? (
                  <Tooltip title="规划规格（手工填写）">
                    <Typography.Text type="secondary">
                      {r.spec.cpu}C / {(r.spec.memMb / 1024).toFixed(0)}G / {r.spec.diskGb}G
                    </Typography.Text>
                  </Tooltip>
                ) : (
                  <Typography.Text type="secondary">未知</Typography.Text>
                ),
            },
            { title: 'CPU', width: 130, render: (_, r) => <UsageProgress used={r.st?.cpuUsage ?? null} total={r.st?.cpuUsage === null || r.st?.cpuUsage === undefined ? null : 1} /> },
            { title: 'Load1', width: 70, render: (_, r) => r.st?.load1?.toFixed(2) ?? '-' },
            {
              title: '内存',
              width: 130,
              render: (_, r) => <UsageProgress used={r.st?.memTotalBytes && r.st.memAvailableBytes !== null ? r.st.memTotalBytes - r.st.memAvailableBytes : null} total={r.st?.memTotalBytes ?? null} />,
            },
            {
              title: '数据盘',
              width: 130,
              render: (_, r) => <UsageProgress used={r.st?.diskTotalBytes && r.st.diskFreeBytes !== null ? r.st.diskTotalBytes - r.st.diskFreeBytes : null} total={r.st?.diskTotalBytes ?? null} />,
            },
            {
              title: '',
              width: 210,
              render: (_, r) => (
                <Space size={4}>
                  {!r.st?.managed && (
                    <ActionButton size="small" action="node-add" params={{ ips: [r.ip] }}>
                      纳管
                    </ActionButton>
                  )}
                  {!r.st?.memTotalBytes && (
                    <Button size="small" icon={<EditOutlined />} onClick={() => setEditing(r.ip)}>
                      规划规格
                    </Button>
                  )}
                  {r.st?.managed && grafana.node(r.ip) && (
                    <Button size="small" type="link" icon={<LineChartOutlined />} href={grafana.node(r.ip)} target="_blank" />
                  )}
                </Space>
              ),
            },
          ]}
        />
      </Card>
      {editing && <SpecDialog ip={editing} spec={nodeSpecs.data?.find((s) => s.ip === editing)} onClose={() => setEditing(undefined)} />}
    </div>
  )
}
