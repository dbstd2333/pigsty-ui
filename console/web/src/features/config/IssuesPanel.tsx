import { Badge, Button, Empty, List, Space, Tag, Typography } from 'antd'
import { CheckCircleOutlined, CloseCircleOutlined, WarningOutlined } from '@ant-design/icons'
import type { Issue } from '@/api/types'

const SOURCE_LABEL: Record<Issue['source'], string> = {
  yaml: '语法',
  schema: '参数',
  semantic: '规则',
  validate: 'bin/validate',
  inventory: 'inventory',
}

interface Props {
  issues: Issue[]
  serverChecking: boolean
  serverCheckedAt?: number
  onServerCheck: () => void
  onJumpLine: (line: number) => void
}

export function IssuesPanel({ issues, serverChecking, serverCheckedAt, onServerCheck, onJumpLine }: Props) {
  const errors = issues.filter((i) => i.level === 'error').length
  const warnings = issues.length - errors
  const sorted = [...issues].sort((a, b) => Number(a.level !== 'error') - Number(b.level !== 'error'))

  return (
    <div style={{ display: 'grid', gap: 8 }}>
      <Space style={{ justifyContent: 'space-between', width: '100%' }}>
        <Space>
          <Badge count={errors} showZero color={errors ? '#ff4d4f' : '#52c41a'} />
          <Typography.Text>错误</Typography.Text>
          <Badge count={warnings} showZero color="#faad14" />
          <Typography.Text>警告</Typography.Text>
        </Space>
        <Button size="small" loading={serverChecking} onClick={onServerCheck}>
          服务端校验
        </Button>
      </Space>
      {serverCheckedAt && (
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          已包含服务端校验结果（bin/validate + inventory）
        </Typography.Text>
      )}
      {sorted.length === 0 ? (
        <Empty image={<CheckCircleOutlined style={{ fontSize: 32, color: '#52c41a' }} />} description="没有发现问题" />
      ) : (
        <List
          size="small"
          dataSource={sorted}
          style={{ maxHeight: 'calc(100vh - 260px)', overflow: 'auto' }}
          renderItem={(i) => (
            <List.Item
              style={{ cursor: i.line ? 'pointer' : 'default', padding: '6px 4px' }}
              onClick={() => i.line && onJumpLine(i.line)}
            >
              <Space align="start" size={6}>
                {i.level === 'error' ? (
                  <CloseCircleOutlined style={{ color: '#ff4d4f', marginTop: 4 }} />
                ) : (
                  <WarningOutlined style={{ color: '#faad14', marginTop: 4 }} />
                )}
                <div>
                  <div style={{ fontSize: 13 }}>{i.message}</div>
                  <Space size={4}>
                    <Tag style={{ fontSize: 10, lineHeight: '16px' }}>{SOURCE_LABEL[i.source]}</Tag>
                    {i.line && (
                      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                        第 {i.line} 行
                      </Typography.Text>
                    )}
                  </Space>
                </div>
              </Space>
            </List.Item>
          )}
        />
      )}
    </div>
  )
}
