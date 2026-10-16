import { useMemo, useState } from 'react'
import { Alert, Input, Select, Space, Switch, Table, Tag, Typography } from 'antd'
import { useEffective } from '@/api/queries'
import type { EffectiveVar } from '@/api/types'
import { allHosts, type ConfigModel } from './model'
import { preview } from './semanticDiff'

const SOURCE_TAG: Record<EffectiveVar['source'], { color: string; label: string }> = {
  host: { color: 'magenta', label: '主机' },
  group: { color: 'blue', label: '组' },
  global: { color: 'green', label: '全局' },
  default: { color: 'default', label: '默认值' },
}

interface Props {
  model: ConfigModel
  savedHash?: string
  dirty: boolean
  host?: string
  onHostChange: (h: string) => void
}

export function EffectiveView({ model, savedHash, dirty, host, onHostChange }: Props) {
  const hosts = allHosts(model)
  const current = host && hosts.includes(host) ? host : hosts[0]
  const q = useEffective(current, savedHash)
  const [filter, setFilter] = useState('')
  const [hideDefault, setHideDefault] = useState(true)
  const [reveal, setReveal] = useState(false)

  const rows = useMemo(() => {
    const s = filter.toLowerCase()
    return (q.data?.vars ?? [])
      .filter((v) => !hideDefault || v.source !== 'default')
      .filter((v) => !s || v.key.includes(s) || JSON.stringify(v.value ?? '').toLowerCase().includes(s))
      .sort((a, b) => a.key.localeCompare(b.key))
  }, [q.data, filter, hideDefault])

  return (
    <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr)', gap: 12 }}>
      {dirty && <Alert type="info" showIcon title="有效值基于已保存的版本计算，不包含当前未保存的修改。" />}
      {q.data?.engine === 'builtin' && (
        <Alert type="warning" showIcon title="本机没有 ansible-inventory，有效值由内置规则计算，组优先级与 Ansible 可能有细微差异。" />
      )}
      <Space wrap>
        <Select
          showSearch
          value={current}
          onChange={onHostChange}
          options={hosts.map((h) => ({ value: h, label: h }))}
          style={{ width: 180 }}
          placeholder="选择主机"
        />
        <Input.Search placeholder="筛选参数名或值" allowClear onChange={(e) => setFilter(e.target.value)} style={{ width: 240 }} />
        <Space size={4}>
          <Switch size="small" checked={hideDefault} onChange={setHideDefault} />
          <Typography.Text>隐藏默认值</Typography.Text>
        </Space>
        <Space size={4}>
          <Switch size="small" checked={reveal} onChange={setReveal} />
          <Typography.Text>显示敏感值</Typography.Text>
        </Space>
      </Space>
      {q.error && <Alert type="error" showIcon title={`有效值计算失败：${(q.error as Error).message}`} />}
      <Table<EffectiveVar>
        size="small"
        rowKey="key"
        loading={q.isLoading}
        dataSource={rows}
        pagination={{ pageSize: 50, showSizeChanger: false }}
        columns={[
          { title: '参数', dataIndex: 'key', width: 280, render: (k: string) => <span className="mono">{k}</span> },
          {
            title: '值',
            dataIndex: 'value',
            render: (v: unknown, r) => (
              <Typography.Text className="mono" style={{ fontSize: 12 }}>
                {preview(v, r.sensitive && !reveal)}
              </Typography.Text>
            ),
          },
          {
            title: '来源',
            dataIndex: 'source',
            width: 160,
            render: (s: EffectiveVar['source'], r) => (
              <Tag color={SOURCE_TAG[s].color}>
                {SOURCE_TAG[s].label}
                {r.sourceGroup ? ` ${r.sourceGroup}` : ''}
              </Tag>
            ),
          },
        ]}
      />
    </div>
  )
}
