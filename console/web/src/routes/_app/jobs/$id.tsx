import { useEffect, useMemo, useRef, useState } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useMutation, useQuery } from '@tanstack/react-query'
import {
  Alert,
  App,
  Badge,
  Button,
  Card,
  Descriptions,
  Empty,
  Input,
  Popconfirm,
  Result,
  Segmented,
  Space,
  Spin,
  Steps,
  Table,
  Tag,
  Tooltip,
  Typography,
} from 'antd'
import { HistoryOutlined, StopOutlined } from '@ant-design/icons'
import dayjs from 'dayjs'
import { api } from '@/api/endpoints'
import { qk } from '@/api/queries'
import type { HostStats } from '@/api/types'
import { RiskTag } from '@/features/actions/RunAction'
import { duration, JobStatusTag, TASK_COLORS } from '@/features/jobs/JobStatusTag'
import { JobFollowUp } from '@/features/jobs/JobFollowUp'
import { useJobStream, type TaskRow } from '@/features/jobs/useJobStream'

export const Route = createFileRoute('/_app/jobs/$id')({
  component: JobDetail,
})

function JobDetail() {
  const { id } = Route.useParams()
  const { message } = App.useApp()
  const navigate = useNavigate()
  const initial = useQuery({ queryKey: qk.job(id), queryFn: () => api.job(id) })
  const stream = useJobStream(id, initial.data)
  const job = stream.job ?? initial.data
  const [tab, setTab] = useState<'matrix' | 'log'>('matrix')
  const [, tick] = useState(0)

  // 运行中时每秒刷新耗时
  useEffect(() => {
    if (job?.status !== 'running') return
    const t = window.setInterval(() => tick((n) => n + 1), 1000)
    return () => clearInterval(t)
  }, [job?.status])

  const cancel = useMutation({
    mutationFn: () => api.cancelJob(id),
    onSuccess: () => message.info('已发送取消信号，等待 ansible 退出'),
    onError: (e) => message.error(`取消失败：${(e as Error).message}`),
  })

  if (initial.isLoading) return <Spin style={{ display: 'block', marginTop: 80 }} />
  if (!job) return <Result status="404" title="作业不存在" subTitle={(initial.error as Error | null)?.message} />

  const running = job.status === 'running' || job.status === 'queued'
  const stats = Object.keys(stream.stats).length ? stream.stats : job.stats

  return (
    <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr)', gap: 12 }}>
      <Card
        size="small"
        title={
          <Space wrap>
            <span>{job.title}</span>
            <JobStatusTag status={job.status} check={job.check} />
            <RiskTag risk={job.risk} />
            {running && <Badge status={stream.connected ? 'processing' : 'default'} text={stream.connected ? '实时' : '连接中…'} />}
          </Space>
        }
        extra={
          <Space>
            {job.restorePoint && job.cluster && (
              <Tooltip title={`执行前已创建恢复点 ${job.restorePoint.name}`}>
                <Button
                  icon={<HistoryOutlined />}
                  onClick={() => navigate({ to: '/clusters/$cls/pitr', params: { cls: job.cluster! }, search: { restorePoint: job.restorePoint!.name } })}
                >
                  回滚到此作业执行前
                </Button>
              </Tooltip>
            )}
            {running && (
              <Popconfirm
                title="取消作业？"
                description="中途中断可能让目标处于不一致状态，之后需要重新执行。"
                onConfirm={() => cancel.mutate()}
              >
                <Button danger icon={<StopOutlined />} loading={cancel.isPending}>
                  取消
                </Button>
              </Popconfirm>
            )}
          </Space>
        }
      >
        <Descriptions
          size="small"
          column={{ xs: 1, md: 2, xl: 4 }}
          items={[
            { label: '作业 ID', children: <span className="mono">{job.id}</span> },
            { label: '动作', children: <span className="mono">{job.action}</span> },
            {
              label: '集群',
              children: job.cluster ? (
                <Link to="/clusters/$cls" params={{ cls: job.cluster }}>
                  {job.cluster}
                </Link>
              ) : (
                '-'
              ),
            },
            { label: '发起人', children: job.user },
            { label: '创建时间', children: dayjs(job.createdAt).format('YYYY-MM-DD HH:mm:ss') },
            { label: '耗时', children: duration(job.startedAt, job.finishedAt) },
            { label: '配置版本', children: <span className="mono">{job.configCommit?.slice(0, 8) ?? job.configHash.slice(0, 8)}</span> },
            { label: '退出码', children: job.exitCode ?? '-' },
            ...(job.confirmText ? [{ label: '确认输入', children: <span className="mono">{job.confirmText}</span> }] : []),
            ...(job.restorePoint
              ? [{ label: '自动恢复点', children: <span className="mono">{job.restorePoint.name} {job.restorePoint.lsn ?? ''}</span> }]
              : []),
          ]}
        />
        {job.error && <Alert type="error" showIcon title={job.error} style={{ marginTop: 8 }} />}
      </Card>

      <JobFollowUp job={job} />

      <Card size="small" title="执行步骤">
        <Steps
          direction="vertical"
          size="small"
          current={job.currentStep}
          items={job.commands.map((c, i) => {
            const s = stream.steps[i]
            const status =
              s?.exitCode !== undefined
                ? s.exitCode === 0
                  ? 'finish'
                  : 'error'
                : i < job.currentStep || (job.status === 'success' && i <= job.currentStep)
                  ? 'finish'
                  : i === job.currentStep
                    ? running
                      ? 'process'
                      : job.status === 'failed'
                        ? 'error'
                        : job.status === 'success'
                          ? 'finish'
                          : 'wait'
                    : 'wait'
            return {
              status,
              title: <code className="mono" style={{ fontSize: 12, wordBreak: 'break-all' }}>{c}</code>,
            }
          })}
        />
      </Card>

      <HostStatsCard stats={stats} />

      <Card
        size="small"
        title={
          <Segmented
            size="small"
            value={tab}
            onChange={(v) => setTab(v as 'matrix' | 'log')}
            options={[
              { value: 'matrix', label: `任务进度（${stream.tasks.length}）` },
              { value: 'log', label: `日志（${stream.logs.length} 行）` },
            ]}
          />
        }
        extra={
          <a href={`/api/v1/jobs/${encodeURIComponent(id)}/log`} target="_blank" rel="noreferrer">
            完整日志
          </a>
        }
      >
        {tab === 'matrix' ? <TaskMatrix tasks={stream.tasks} hosts={stream.hosts} /> : <LogView lines={stream.logs} follow={running} />}
      </Card>
    </div>
  )
}

function HostStatsCard({ stats }: { stats: Record<string, HostStats> }) {
  const rows = Object.entries(stats).map(([host, s]) => ({ host, ...s }))
  if (!rows.length) return null
  return (
    <Card size="small" title="主机统计">
      <Table
        size="small"
        rowKey="host"
        pagination={false}
        dataSource={rows}
        columns={[
          { title: '主机', dataIndex: 'host', render: (h) => <span className="mono">{h}</span> },
          { title: 'ok', dataIndex: 'ok', render: (n) => <span style={{ color: TASK_COLORS.ok }}>{n}</span> },
          { title: 'changed', dataIndex: 'changed', render: (n) => <span style={{ color: TASK_COLORS.changed }}>{n}</span> },
          { title: 'failed', dataIndex: 'failed', render: (n) => <span style={{ color: n ? TASK_COLORS.failed : undefined }}>{n}</span> },
          { title: 'unreachable', dataIndex: 'unreachable', render: (n) => <span style={{ color: n ? TASK_COLORS.unreachable : undefined }}>{n}</span> },
          { title: 'skipped', dataIndex: 'skipped' },
        ]}
      />
    </Card>
  )
}

function TaskMatrix({ tasks, hosts }: { tasks: TaskRow[]; hosts: string[] }) {
  const [onlyProblem, setOnlyProblem] = useState(false)
  const rows = useMemo(
    () => (onlyProblem ? tasks.filter((t) => Object.values(t.results).some((r) => r.status === 'failed' || r.status === 'unreachable' || r.status === 'changed')) : tasks),
    [tasks, onlyProblem],
  )
  if (!tasks.length) return <Empty description="还没有任务事件" />
  return (
    <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr)', gap: 8 }}>
      <Space>
        <Segmented
          size="small"
          value={onlyProblem ? 'changed' : 'all'}
          onChange={(v) => setOnlyProblem(v === 'changed')}
          options={[
            { value: 'all', label: '全部任务' },
            { value: 'changed', label: '仅变更/失败' },
          ]}
        />
        {Object.entries(TASK_COLORS).map(([k, c]) => (
          <Space key={k} size={4}>
            <span style={{ display: 'inline-block', width: 10, height: 10, borderRadius: 2, background: c }} />
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {k}
            </Typography.Text>
          </Space>
        ))}
      </Space>
      <Table<TaskRow>
        size="small"
        rowKey="key"
        pagination={false}
        scroll={{ x: 'max-content', y: 'calc(100vh - 420px)' }}
        dataSource={rows}
        columns={[
          {
            title: '任务',
            fixed: 'left',
            width: 360,
            render: (_, t) => (
              <Tooltip title={`${t.playbook} · ${t.play}`}>
                <span style={{ fontSize: 12 }}>{t.task}</span>
              </Tooltip>
            ),
          },
          ...hosts.map((h) => ({
            title: <span className="mono" style={{ fontSize: 11 }}>{h}</span>,
            width: 110,
            align: 'center' as const,
            render: (_: unknown, t: TaskRow) => {
              const r = t.results[h]
              if (!r) return null
              return (
                <Tooltip title={r.msg ? `${r.status}：${r.msg}` : `${r.status}${r.durationMs ? ` · ${r.durationMs} ms` : ''}`}>
                  <span style={{ display: 'inline-block', width: 14, height: 14, borderRadius: 3, background: TASK_COLORS[r.status] }} />
                </Tooltip>
              )
            },
          })),
        ]}
      />
    </div>
  )
}

function LogView({ lines, follow }: { lines: string[]; follow: boolean }) {
  const ref = useRef<HTMLPreElement>(null)
  const [filter, setFilter] = useState('')
  const [auto, setAuto] = useState(true)
  const shown = useMemo(() => (filter ? lines.filter((l) => l.toLowerCase().includes(filter.toLowerCase())) : lines), [lines, filter])

  useEffect(() => {
    if (auto && follow && ref.current) ref.current.scrollTop = ref.current.scrollHeight
  }, [shown, auto, follow])

  return (
    <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr)', gap: 8 }}>
      <Space>
        <Input.Search size="small" allowClear placeholder="过滤日志" onSearch={setFilter} style={{ width: 260 }} />
        {follow && (
          <Tag.CheckableTag checked={auto} onChange={setAuto}>
            自动滚动
          </Tag.CheckableTag>
        )}
      </Space>
      <pre
        ref={ref}
        className="mono"
        onWheel={() => follow && setAuto(false)}
        style={{
          background: '#1f1f1f',
          color: '#d9d9d9',
          margin: 0,
          padding: 10,
          borderRadius: 6,
          fontSize: 12,
          lineHeight: 1.5,
          height: 'calc(100vh - 400px)',
          minHeight: 300,
          overflow: 'auto',
          whiteSpace: 'pre-wrap',
          wordBreak: 'break-all',
        }}
      >
        {shown.map((l, i) => (
          <div key={i} style={{ color: /fatal|failed|unreachable|error/i.test(l) ? '#ff7875' : /changed/.test(l) ? '#ffd666' : /^ok:/.test(l) ? '#95de64' : undefined }}>
            {l || ' '}
          </div>
        ))}
      </pre>
    </div>
  )
}
