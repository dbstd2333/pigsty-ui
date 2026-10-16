import { useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { Card, Select, Space, Table, Tag, Typography } from 'antd'
import dayjs from 'dayjs'
import { useJobs } from '@/api/queries'
import type { Job } from '@/api/types'
import { RiskTag } from '@/features/actions/RunAction'
import { duration, JobStatusTag } from '@/features/jobs/JobStatusTag'
import { useSavedModel } from '@/features/config/useSavedModel'

interface Search {
  cluster?: string
  status?: string
}

export const Route = createFileRoute('/_app/jobs/')({
  validateSearch: (s: Record<string, unknown>): Search => ({
    cluster: typeof s.cluster === 'string' ? s.cluster : undefined,
    status: typeof s.status === 'string' ? s.status : undefined,
  }),
  component: JobsPage,
})

const PAGE = 20

function JobsPage() {
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  const [page, setPage] = useState(1)
  const jobs = useJobs({ limit: PAGE, offset: (page - 1) * PAGE, cluster: search.cluster, status: search.status })
  const { model } = useSavedModel()

  return (
    <Card
      size="small"
      title="作业"
      extra={
        <Space>
          <Select
            allowClear
            placeholder="集群"
            style={{ width: 160 }}
            value={search.cluster}
            options={model?.groups.filter((g) => g.module).map((g) => ({ value: g.name, label: g.name }))}
            onChange={(v) => (setPage(1), navigate({ search: (p) => ({ ...p, cluster: v }) }))}
          />
          <Select
            allowClear
            placeholder="状态"
            style={{ width: 120 }}
            value={search.status}
            options={[
              { value: 'running', label: '运行中' },
              { value: 'success', label: '成功' },
              { value: 'failed', label: '失败' },
              { value: 'canceled', label: '已取消' },
            ]}
            onChange={(v) => (setPage(1), navigate({ search: (p) => ({ ...p, status: v }) }))}
          />
        </Space>
      }
    >
      <Table<Job>
        size="small"
        rowKey="id"
        loading={jobs.isLoading}
        dataSource={jobs.data?.items}
        pagination={{ current: page, pageSize: PAGE, total: jobs.data?.total, onChange: setPage, showSizeChanger: false }}
        columns={[
          {
            title: '作业',
            render: (_, j) => (
              <div>
                <Link to="/jobs/$id" params={{ id: j.id }}>
                  {j.title}
                </Link>
                <div>
                  <Typography.Text type="secondary" className="mono" style={{ fontSize: 12 }}>
                    {j.id} · {j.action}
                  </Typography.Text>
                </div>
              </div>
            ),
          },
          { title: '状态', width: 150, render: (_, j) => <JobStatusTag status={j.status} check={j.check} /> },
          { title: '风险', width: 90, render: (_, j) => <RiskTag risk={j.risk} /> },
          {
            title: '集群',
            width: 140,
            render: (_, j) =>
              j.cluster ? (
                <Link to="/clusters/$cls" params={{ cls: j.cluster }}>
                  {j.cluster}
                </Link>
              ) : (
                <Tag>{j.targets.length} 台主机</Tag>
              ),
          },
          { title: '发起人', dataIndex: 'user', width: 90 },
          {
            title: '开始时间',
            width: 160,
            render: (_, j) => (j.startedAt || j.createdAt ? dayjs(j.startedAt ?? j.createdAt).format('MM-DD HH:mm:ss') : '-'),
          },
          { title: '耗时', width: 100, render: (_, j) => duration(j.startedAt, j.finishedAt) },
        ]}
      />
    </Card>
  )
}
