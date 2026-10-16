import { useMemo, useState } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Alert, Button, Card, Col, Descriptions, Input, Popover, Row, Space, Spin, Statistic, Table, Tag, Tooltip, Typography } from 'antd'
import { FlagOutlined, HistoryOutlined, ReloadOutlined } from '@ant-design/icons'
import ReactEChartsCore from 'echarts-for-react/esm/core'
import type { EChartsOption } from 'echarts'
import dayjs from 'dayjs'
import { api } from '@/api/endpoints'
import { qk, useBackups, useSettings } from '@/api/queries'
import type { Backup, BackupInfo } from '@/api/types'
import { ActionButton } from '@/features/actions/RunAction'
import { useGrafana } from '@/features/status/grafana'
import { echarts } from '@/lib/echarts'
import { fmtBytes } from '@/lib/format'

export const Route = createFileRoute('/_app/clusters/$cls/backup')({
  component: BackupPage,
})

const TYPE_META: Record<Backup['type'], { label: string; color: string; h: number }> = {
  full: { label: '全量', color: '#1677ff', h: 3 },
  diff: { label: '差异', color: '#13c2c2', h: 2 },
  incr: { label: '增量', color: '#95de64', h: 1 },
}

function BackupPage() {
  const { cls } = Route.useParams()
  const backups = useBackups(cls)
  const settings = useSettings()
  const grafana = useGrafana()
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const refresh = useMutation({
    mutationFn: () => api.backups(cls, true),
    onSuccess: (d) => queryClient.setQueryData(qk.backups(cls), d),
  })
  const info = backups.data
  const maxAge = settings.data?.backupMaxAgeHours ?? 26

  const toPitr = (search: { time?: string; set?: string; restorePoint?: string }) => navigate({ to: '/clusters/$cls/pitr', params: { cls }, search })

  return (
    <div style={{ display: 'grid', gap: 12 }}>
      <Space style={{ justifyContent: 'space-between', width: '100%' }} wrap>
        <Space>
          <Typography.Title level={4} style={{ margin: 0 }}>
            <Link to="/clusters/$cls" params={{ cls }}>
              {cls}
            </Link>{' '}
            / 备份与恢复
          </Typography.Title>
          {info && (
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              数据时间 {dayjs(info.fetchedAt).format('HH:mm:ss')}
            </Typography.Text>
          )}
        </Space>
        <Space wrap>
          <Button icon={<ReloadOutlined />} loading={refresh.isPending} onClick={() => refresh.mutate()}>
            刷新
          </Button>
          <ActionButton action="backup-run" params={{ cluster: cls }} lockParams={false}>
            立即备份
          </ActionButton>
          <ActionButton action="backup-check" params={{ cluster: cls }}>
            检查归档
          </ActionButton>
          <RestorePointButton cls={cls} />
          <Button type="primary" icon={<HistoryOutlined />} onClick={() => toPitr({})}>
            时间点恢复（PITR）
          </Button>
          {grafana.pitr(cls) && (
            <Button href={grafana.pitr(cls)} target="_blank">
              Grafana
            </Button>
          )}
        </Space>
      </Space>

      {backups.isLoading ? (
        <Spin style={{ display: 'block', marginTop: 40 }} />
      ) : !info ? (
        <Alert type="error" showIcon title={`读取备份信息失败：${(backups.error as Error | null)?.message ?? ''}`} />
      ) : (
        <>
          {!info.source.ok && <Alert type="warning" showIcon title={`pgbackrest info 执行失败：${info.source.error ?? ''}`} />}
          <StatusCards info={info} maxAge={maxAge} />
          <Card size="small" title="备份时间线" extra={<Typography.Text type="secondary" style={{ fontSize: 12 }}>点击图上任意时间点，作为恢复目标打开 PITR 向导</Typography.Text>}>
            <Timeline info={info} onPick={(time) => toPitr({ time })} />
          </Card>
          <Card size="small" title={`备份集（${info.backups.length}）`}>
            <BackupTable info={info} onRestore={(set) => toPitr({ set })} />
          </Card>
          <Card size="small" title={`恢复点（${info.restorePoints.length}）`}>
            <Table
              size="small"
              rowKey="name"
              pagination={false}
              dataSource={[...info.restorePoints].reverse()}
              locale={{ emptyText: '没有恢复点。高风险作业执行前会自动创建 console_job_<id> 恢复点。' }}
              columns={[
                { title: '名称', render: (_, r) => <span className="mono">{r.name}</span> },
                { title: '时间', render: (_, r) => dayjs(r.time).format('YYYY-MM-DD HH:mm:ss') },
                { title: 'LSN', render: (_, r) => <span className="mono">{r.lsn ?? '-'}</span> },
                {
                  title: '来源',
                  render: (_, r) =>
                    r.jobId ? (
                      <Link to="/jobs/$id" params={{ id: r.jobId }}>
                        作业 {r.jobId}
                      </Link>
                    ) : (
                      '手动'
                    ),
                },
                { width: 120, render: (_, r) => <Button size="small" onClick={() => toPitr({ restorePoint: r.name })}>恢复到此处</Button> },
              ]}
            />
          </Card>
        </>
      )}
    </div>
  )
}

function RestorePointButton({ cls }: { cls: string }) {
  const [name, setName] = useState(`manual_${dayjs().format('YYYYMMDD_HHmm')}`)
  return (
    <Popover
      trigger="click"
      title="创建恢复点"
      content={
        <Space.Compact>
          <Input className="mono" value={name} onChange={(e) => setName(e.target.value.trim())} style={{ width: 220 }} />
          <ActionButton action="restore-point" params={{ cluster: cls, name }}>
            创建
          </ActionButton>
        </Space.Compact>
      }
    >
      <Button icon={<FlagOutlined />}>创建恢复点</Button>
    </Popover>
  )
}

function StatusCards({ info, maxAge }: { info: BackupInfo; maxAge: number }) {
  const ok = info.backups.filter((b) => !b.error)
  const lastFull = [...ok].reverse().find((b) => b.type === 'full')
  const last = ok[ok.length - 1]
  const age = (t?: string) => (t ? dayjs().diff(dayjs(t), 'hour', true) : undefined)
  const rpoSec = info.archive.lastArchivedAt ? dayjs().diff(dayjs(info.archive.lastArchivedAt), 'second') : undefined
  const total = ok.reduce((n, b) => n + b.repoDeltaSize, 0)
  const color = (h?: number) => (h === undefined ? undefined : h > maxAge ? '#cf1322' : h > maxAge * 0.75 ? '#d48806' : '#389e0d')

  return (
    <Row gutter={12}>
      <Col span={8}>
        <Card size="small" title="仓库" style={{ height: '100%' }}>
          <Descriptions
            size="small"
            column={1}
            items={[
              { label: '方式', children: <Tag>{info.repo.method}</Tag> },
              { label: '类型 / 位置', children: <span className="mono">{info.repo.type} {info.repo.path ?? ''}</span> },
              { label: '保留策略', children: info.repo.retention ?? '-' },
              { label: '加密', children: info.repo.cipher ?? '未加密' },
              { label: 'stanza', children: <span className="mono">{info.stanza}</span> },
              ...(info.status && info.status.code !== 0 ? [{ label: '状态', children: <Typography.Text type="danger">{info.status.message}</Typography.Text> }] : []),
            ]}
          />
        </Card>
      </Col>
      <Col span={16}>
        <Row gutter={[12, 12]}>
          <Col span={8}>
            <Card size="small">
              <Statistic
                title="最近全量备份"
                value={lastFull ? dayjs(lastFull.stop).fromNow() : '无'}
                valueStyle={{ color: color(age(lastFull?.stop)), fontSize: 20 }}
              />
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {lastFull ? dayjs(lastFull.stop).format('MM-DD HH:mm') : ''}
              </Typography.Text>
            </Card>
          </Col>
          <Col span={8}>
            <Card size="small">
              <Statistic title="最近一次备份" value={last ? dayjs(last.stop).fromNow() : '无'} valueStyle={{ color: color(age(last?.stop)), fontSize: 20 }} />
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {last ? `${TYPE_META[last.type].label} · ${dayjs(last.stop).format('MM-DD HH:mm')}` : `阈值 ${maxAge} 小时`}
              </Typography.Text>
            </Card>
          </Col>
          <Col span={8}>
            <Card size="small">
              <Statistic
                title={<Tooltip title="当前时间减去最后一次归档 WAL 的时间，近似可能丢失的数据量">RPO（归档延迟）</Tooltip>}
                value={rpoSec === undefined ? '-' : rpoSec < 120 ? `${rpoSec} 秒` : dayjs(info.archive.lastArchivedAt).fromNow(true)}
                valueStyle={{ color: rpoSec === undefined ? undefined : rpoSec > 900 ? '#cf1322' : rpoSec > 300 ? '#d48806' : '#389e0d', fontSize: 20 }}
              />
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                最新 WAL <span className="mono">{info.archive.max ?? '-'}</span>
              </Typography.Text>
            </Card>
          </Col>
          <Col span={8}>
            <Card size="small">
              <Statistic title="可恢复窗口" value={info.window ? dayjs(info.window.end).from(dayjs(info.window.start), true) : '无'} valueStyle={{ fontSize: 20 }} />
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {info.window ? `${dayjs(info.window.start).format('MM-DD HH:mm')} → ${dayjs(info.window.end).format('MM-DD HH:mm')}` : ''}
              </Typography.Text>
            </Card>
          </Col>
          <Col span={8}>
            <Card size="small">
              <Statistic
                title="归档失败"
                value={info.archive.failedCount ?? '-'}
                valueStyle={{ color: info.archive.failedCount ? '#cf1322' : undefined, fontSize: 20 }}
              />
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {info.archive.lastFailedAt ? `最近 ${dayjs(info.archive.lastFailedAt).format('MM-DD HH:mm')}` : ''}
              </Typography.Text>
            </Card>
          </Col>
          <Col span={8}>
            <Card size="small">
              <Statistic title="仓库占用（估算）" value={fmtBytes(total)} valueStyle={{ fontSize: 20 }} />
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {ok.length} 个备份集
              </Typography.Text>
            </Card>
          </Col>
        </Row>
      </Col>
    </Row>
  )
}

function Timeline({ info, onPick }: { info: BackupInfo; onPick: (time: string) => void }) {
  const option = useMemo<EChartsOption>(() => {
    const ok = info.backups
    const now = Date.now()
    const start = Math.min(...ok.map((b) => +new Date(b.start)), info.window ? +new Date(info.window.start) : now) - 3600_000 * 6
    const byLabel = new Map(ok.map((b) => [b.label, b]))
    return {
      grid: { left: 40, right: 24, top: 30, bottom: 50 },
      tooltip: {
        trigger: 'item',
        formatter: (p: unknown) => {
          const d = (p as { data?: { b?: Backup; name?: string } }).data
          if (d?.b) {
            const b = d.b
            return `${TYPE_META[b.type].label}备份 ${b.label}<br/>${dayjs(b.start).format('MM-DD HH:mm:ss')} → ${dayjs(b.stop).format('HH:mm:ss')}<br/>数据库 ${fmtBytes(b.size)}，仓库增量 ${fmtBytes(b.repoDeltaSize)}`
          }
          return d?.name ?? ''
        },
      },
      xAxis: { type: 'time', min: start, max: now + 3600_000 },
      yAxis: { type: 'value', min: 0, max: 4.5, show: false },
      dataZoom: [{ type: 'inside' }, { type: 'slider', height: 18, bottom: 8 }],
      series: [
        {
          type: 'custom',
          name: 'backups',
          renderItem: (_params, api) => {
            const s = api.coord([api.value(0), api.value(2)])
            const e = api.coord([api.value(1), 0])
            const color = api.value(3) as unknown as string
            return {
              type: 'rect',
              shape: { x: s[0], y: s[1], width: Math.max(e[0] - s[0], 4), height: e[1] - s[1] },
              style: { fill: color, opacity: api.value(4) ? 0.3 : 0.85 },
            }
          },
          encode: { x: [0, 1], y: 2 },
          data: ok.map((b) => ({ value: [+new Date(b.start), +new Date(b.stop), TYPE_META[b.type].h, TYPE_META[b.type].color, b.error ? 1 : 0], b })),
          z: 3,
        },
        {
          // 依赖链：增量/差异备份指向其父备份
          type: 'lines',
          coordinateSystem: 'cartesian2d',
          polyline: false,
          lineStyle: { color: '#bfbfbf', width: 1, type: 'dashed', curveness: 0.3 },
          data: ok
            .filter((b) => b.prior && byLabel.has(b.prior))
            .map((b) => {
              const p = byLabel.get(b.prior!)!
              return { coords: [[+new Date(b.start), TYPE_META[b.type].h], [+new Date(p.stop), TYPE_META[p.type].h]] }
            }),
          z: 2,
        },
        {
          // 只承载窗口、恢复点、事件标注；点击由 zrender 层统一处理
          type: 'scatter',
          name: 'marks',
          data: [],
          markArea: info.window
            ? {
                silent: true,
                itemStyle: { color: 'rgba(22,119,255,0.08)' },
                label: { show: true, position: 'insideTop', color: '#1677ff', fontSize: 11 },
                data: [[{ name: '可恢复窗口', xAxis: +new Date(info.window.start) }, { xAxis: +new Date(info.window.end) }]],
              }
            : undefined,
          markPoint: {
            symbol: 'pin',
            symbolSize: 26,
            label: { show: false },
            itemStyle: { color: '#fa8c16' },
            data: info.restorePoints.map((r) => ({ name: `恢复点 ${r.name}\n${dayjs(r.time).format('MM-DD HH:mm:ss')}`, coord: [+new Date(r.time), 4] })),
          },
          markLine: {
            symbol: 'none',
            label: { formatter: '{b}', fontSize: 10 },
            lineStyle: { type: 'solid' },
            data: [
              ...info.events.map((ev) => ({
                name: ev.label,
                xAxis: +new Date(ev.time),
                lineStyle: { color: ev.kind === 'pitr' ? '#cf1322' : ev.kind === 'failover' ? '#d48806' : '#8c8c8c' },
              })),
              { name: '现在', xAxis: now, lineStyle: { color: '#52c41a', type: 'dashed' } },
            ],
          },
        },
      ],
    } as EChartsOption
  }, [info])

  return (
    <ReactEChartsCore
      echarts={echarts}
      option={option}
      style={{ height: 240 }}
      onChartReady={(chart) => {
        chart.getZr().on('click', (e: { offsetX: number; offsetY: number }) => {
          const pt = [e.offsetX, e.offsetY]
          if (!chart.containPixel('grid', pt)) return
          const [x] = chart.convertFromPixel('grid', pt) as number[]
          if (info.window && (x < +new Date(info.window.start) || x > +new Date(info.window.end))) return
          onPick(new Date(Math.round(x / 1000) * 1000).toISOString())
        })
      }}
    />
  )
}

function BackupTable({ info, onRestore }: { info: BackupInfo; onRestore: (set: string) => void }) {
  return (
    <Table<Backup>
      size="small"
      rowKey="label"
      pagination={{ pageSize: 20, hideOnSinglePage: true }}
      dataSource={[...info.backups].reverse()}
      scroll={{ x: 'max-content' }}
      columns={[
        {
          title: '备份集',
          render: (_, b) => (
            <Space direction="vertical" size={0}>
              <span className="mono" style={{ fontSize: 12 }}>
                {b.label}
              </span>
              {b.prior && (
                <Typography.Text type="secondary" className="mono" style={{ fontSize: 11 }}>
                  ← {b.prior}
                </Typography.Text>
              )}
            </Space>
          ),
        },
        {
          title: '类型',
          width: 70,
          render: (_, b) => (
            <Tag color={b.error ? 'red' : undefined} style={{ borderColor: TYPE_META[b.type].color, color: TYPE_META[b.type].color }}>
              {TYPE_META[b.type].label}
            </Tag>
          ),
        },
        { title: '开始', width: 140, render: (_, b) => dayjs(b.start).format('MM-DD HH:mm:ss') },
        { title: '耗时', width: 80, render: (_, b) => `${Math.max(1, dayjs(b.stop).diff(dayjs(b.start), 'second'))} 秒` },
        { title: '数据库大小', width: 100, align: 'right', render: (_, b) => fmtBytes(b.size) },
        { title: '备份大小', width: 100, align: 'right', render: (_, b) => fmtBytes(b.repoSize) },
        { title: '仓库增量', width: 100, align: 'right', render: (_, b) => fmtBytes(b.repoDeltaSize) },
        { title: 'LSN', render: (_, b) => <span className="mono" style={{ fontSize: 11 }}>{b.lsnStart ?? '-'} → {b.lsnStop ?? '-'}</span> },
        { title: 'WAL', render: (_, b) => <span className="mono" style={{ fontSize: 11 }}>{b.walStart ?? '-'} → {b.walStop ?? '-'}</span> },
        { width: 100, fixed: 'right', render: (_, b) => !b.error && <Button size="small" onClick={() => onRestore(b.label)}>恢复此备份</Button> },
      ]}
    />
  )
}
