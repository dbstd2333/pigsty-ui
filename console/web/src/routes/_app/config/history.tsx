import { lazy, Suspense, useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { App, Button, Card, Empty, Popconfirm, Radio, Space, Spin, Table, Tag, Typography } from 'antd'
import { RollbackOutlined } from '@ant-design/icons'
import dayjs from 'dayjs'
import { api } from '@/api/endpoints'
import { ApiError } from '@/api/http'
import { qk, useConfig, useHistory } from '@/api/queries'
import type { CommitInfo } from '@/api/types'

const DiffView = lazy(() => import('@/features/config/DiffView'))

export const Route = createFileRoute('/_app/config/history')({
  component: HistoryPage,
})

const PAGE = 30

function HistoryPage() {
  const { message } = App.useApp()
  const queryClient = useQueryClient()
  const [page, setPage] = useState(1)
  const history = useHistory(PAGE, (page - 1) * PAGE)
  const config = useConfig()
  const [selected, setSelected] = useState<string>()
  const [mode, setMode] = useState<'prev' | 'head'>('prev')

  const items = history.data?.items ?? []
  const idx = items.findIndex((c) => c.hash === selected)
  const commit = idx >= 0 ? items[idx] : undefined
  // 「本次改动」与上一个版本比较；列表最后一项的上一版本可能在下一页，此时与 HEAD 比较
  const prev = idx >= 0 ? items[idx + 1] : undefined
  const from = mode === 'prev' ? prev?.hash : commit?.hash
  const to = mode === 'prev' ? commit?.hash : 'HEAD'

  const diff = useQuery({
    queryKey: qk.diff(from ?? '', to ?? ''),
    queryFn: () => api.diff(from!, to!),
    enabled: !!from && !!to,
  })

  const revert = async (c: CommitInfo) => {
    if (!config.data) return
    try {
      await api.revert(c.hash, config.data.hash, `回滚到 ${c.shortHash}：${c.message}`)
      message.success(`已回滚到 ${c.shortHash}，产生了一个新版本`)
      await queryClient.invalidateQueries({ queryKey: qk.config })
      await queryClient.invalidateQueries({ queryKey: ['inventory'] })
    } catch (e) {
      if (e instanceof ApiError && e.status === 422) message.error('该版本未通过当前的校验，无法回滚')
      else if (e instanceof ApiError && e.status === 409) message.error('配置刚刚被修改过，请刷新后再试')
      else message.error(`回滚失败：${(e as Error).message}`)
    }
  }

  return (
    <div style={{ display: 'grid', gridTemplateColumns: 'minmax(420px, 2fr) 3fr', gap: 12, alignItems: 'start' }}>
      <Card title="配置历史" size="small">
        <Table<CommitInfo>
          size="small"
          rowKey="hash"
          loading={history.isLoading}
          dataSource={items}
          onRow={(r) => ({ onClick: () => setSelected(r.hash), style: { cursor: 'pointer' } })}
          rowClassName={(r) => (r.hash === selected ? 'ant-table-row-selected' : '')}
          pagination={{ current: page, pageSize: PAGE, total: history.data?.total, onChange: setPage, showSizeChanger: false }}
          columns={[
            {
              title: '时间',
              dataIndex: 'time',
              width: 110,
              render: (t: string) => (
                <Typography.Text title={dayjs(t).format('YYYY-MM-DD HH:mm:ss')}>{dayjs(t).fromNow()}</Typography.Text>
              ),
            },
            {
              title: '说明',
              dataIndex: 'message',
              render: (m: string, r) => (
                <div>
                  <div>{m}</div>
                  <Space size={4}>
                    <span className="mono" style={{ fontSize: 12, color: '#888' }}>
                      {r.shortHash}
                    </span>
                    {r.author === 'external' ? <Tag color="orange">外部修改</Tag> : <Tag>{r.author}</Tag>}
                    {r.hash === config.data?.commit?.hash && <Tag color="blue">当前</Tag>}
                  </Space>
                </div>
              ),
            },
          ]}
        />
      </Card>

      <Card
        size="small"
        title={commit ? `${commit.shortHash} · ${commit.message}` : '版本详情'}
        extra={
          commit && (
            <Space>
              <Radio.Group size="small" value={mode} onChange={(e) => setMode(e.target.value)} optionType="button">
                <Radio.Button value="prev" disabled={!prev}>
                  本次改动
                </Radio.Button>
                <Radio.Button value="head">与当前版本比较</Radio.Button>
              </Radio.Group>
              <Popconfirm
                title={`回滚到 ${commit.shortHash}？`}
                description="会把配置文件恢复成这个版本的内容并产生一个新版本；已运行的集群不会自动改变。"
                onConfirm={() => revert(commit)}
                disabled={commit.hash === config.data?.commit?.hash}
              >
                <Button size="small" icon={<RollbackOutlined />} disabled={commit.hash === config.data?.commit?.hash}>
                  回滚到此版本
                </Button>
              </Popconfirm>
            </Space>
          )
        }
      >
        {!commit ? (
          <Empty description="在左侧选择一个版本" />
        ) : !from || !to ? (
          <Empty description="这是最早的版本，或上一版本在下一页；可切换为「与当前版本比较」" />
        ) : diff.isLoading ? (
          <Spin />
        ) : diff.data ? (
          diff.data.fromText === diff.data.toText ? (
            <Empty description="内容相同" />
          ) : (
            <Suspense fallback={<Spin />}>
              <DiffView
                original={diff.data.fromText}
                modified={diff.data.toText}
                height="calc(100vh - 200px)"
                originalTitle={mode === 'prev' ? '上一版本' : `版本 ${commit.shortHash}`}
                modifiedTitle={mode === 'prev' ? `版本 ${commit.shortHash}` : '当前版本'}
              />
            </Suspense>
          )
        ) : (
          <Typography.Text type="danger">{(diff.error as Error | null)?.message}</Typography.Text>
        )}
      </Card>
    </div>
  )
}
