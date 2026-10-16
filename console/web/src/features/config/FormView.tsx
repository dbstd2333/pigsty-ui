import { useMemo, useState } from 'react'
import { App, AutoComplete, Button, Card, Empty, Input, Popconfirm, Space, Table, Tag, Tooltip, Typography } from 'antd'
import { CodeOutlined, DeleteOutlined, LinkOutlined, PlusOutlined, QuestionCircleOutlined } from '@ant-design/icons'
import type { ParamDef, ParamSchema } from '@/api/types'
import { compareIp, isSensitiveKey, scopeLabel, scopePath, scopeVars, type ConfigModel, type Scope } from './model'
import { ObjectListEditor, ObjectMapEditor, objectDefOf } from './ObjectEditors'
import { editorKind, emptyValue, ValueEditor } from './ValueEditor'
import { deleteIn, setIn, type Path } from './yamlPatch'

interface Props {
  text: string
  model: ConfigModel
  scope: Scope
  schema?: ParamSchema
  onChange: (text: string) => void
  onJump: (path: Path) => void
}

interface Row {
  key: string
  value: unknown
  def?: ParamDef
}

const IPV4 = /^(\d{1,3}\.){3}\d{1,3}$/

export function FormView({ text, model, scope, schema, onChange, onJump }: Props) {
  const { message } = App.useApp()
  const vars = scopeVars(model, scope)
  const base = scopePath(scope)

  const apply = (fn: () => string) => {
    try {
      onChange(fn())
    } catch (e) {
      message.error(`修改失败：${(e as Error).message}`)
    }
  }

  // 按模块/段落分组展示
  const sections = useMemo(() => {
    const map = new Map<string, Row[]>()
    for (const [key, value] of Object.entries(vars)) {
      const def = schema?.params[key]
      const title = def ? `${def.module}${def.section ? ' · ' + def.section : ''}` : '其他'
      if (!map.has(title)) map.set(title, [])
      map.get(title)!.push({ key, value, def })
    }
    return [...map.entries()].sort(([a], [b]) => (a === '其他' ? 1 : b === '其他' ? -1 : a.localeCompare(b)))
  }, [vars, schema])

  return (
    <div style={{ display: 'grid', gap: 12 }}>
      <Space style={{ justifyContent: 'space-between', width: '100%' }} wrap>
        <Typography.Title level={5} style={{ margin: 0 }}>
          {scopeLabel(scope)}
          <Typography.Text type="secondary" style={{ fontSize: 12, fontWeight: 400, marginInlineStart: 8 }}>
            {Object.keys(vars).length} 个参数
          </Typography.Text>
        </Typography.Title>
        <AddParam schema={schema} existing={vars} onAdd={(k, v) => apply(() => setIn(text, [...base, k], v))} />
      </Space>

      {scope.kind === 'group' && (
        <HostsCard text={text} model={model} group={scope.group} onChange={onChange} />
      )}

      {sections.length === 0 && <Empty description="这里还没有参数，用右上角「添加参数」添加" />}
      {sections.map(([title, rows]) => (
        <Card key={title} size="small" title={title}>
          <Table<Row>
            size="small"
            rowKey="key"
            pagination={false}
            showHeader={false}
            dataSource={rows}
            columns={[
              {
                width: 280,
                render: (_, r) => <ParamName row={r} />,
              },
              {
                render: (_, r) => {
                  const sensitive = r.def?.sensitive || isSensitiveKey(r.key)
                  const kind = editorKind(r.def, r.value, !!sensitive)
                  const od = kind === 'complex' ? objectDefOf(r.def, schema) : undefined
                  if (od?.mode === 'list' && Array.isArray(r.value)) {
                    return <ObjectListEditor text={text} path={[...base, r.key]} value={r.value as Record<string, unknown>[]} def={od.def} title={r.key} onChange={onChange} />
                  }
                  if (od?.mode === 'map' && r.value && typeof r.value === 'object' && !Array.isArray(r.value)) {
                    return <ObjectMapEditor text={text} path={[...base, r.key]} value={r.value as Record<string, Record<string, unknown>>} def={od.def} title={r.key} onChange={onChange} />
                  }
                  if (kind === 'complex') {
                    return (
                      <Space>
                        <Typography.Text type="secondary" className="mono" ellipsis style={{ maxWidth: 420 }}>
                          {Array.isArray(r.value) ? `${r.value.length} 项` : `${Object.keys(r.value as object).length} 个字段`}
                        </Typography.Text>
                        <Button size="small" icon={<CodeOutlined />} onClick={() => onJump([...base, r.key])}>
                          在源码中编辑
                        </Button>
                      </Space>
                    )
                  }
                  return (
                    <ValueEditor
                      kind={kind}
                      def={r.def}
                      value={r.value}
                      onCommit={(v) => apply(() => setIn(text, [...base, r.key], v))}
                    />
                  )
                },
              },
              {
                width: 80,
                align: 'right',
                render: (_, r) => (
                  <Space size={0}>
                    <Tooltip title="定位到源码">
                      <Button type="text" size="small" icon={<CodeOutlined />} onClick={() => onJump([...base, r.key])} />
                    </Tooltip>
                    <Popconfirm title={`删除 ${r.key}？`} description="删除后将使用上一级或默认值" onConfirm={() => apply(() => deleteIn(text, [...base, r.key]))}>
                      <Button type="text" size="small" danger icon={<DeleteOutlined />} />
                    </Popconfirm>
                  </Space>
                ),
              },
            ]}
          />
        </Card>
      ))}
    </div>
  )
}

function ParamName({ row }: { row: Row }) {
  const { key, def, value } = row
  const isDefault = def?.default !== undefined && JSON.stringify(def.default) === JSON.stringify(value)
  return (
    <Space size={4} wrap>
      <span className="mono">{key}</span>
      {def?.label && <Typography.Text type="secondary">{def.label}</Typography.Text>}
      {def?.level && <Tag style={{ fontSize: 10, lineHeight: '16px', marginInlineEnd: 0 }}>{def.level}</Tag>}
      {isDefault && <Tag color="default" style={{ fontSize: 10, lineHeight: '16px' }}>同默认值</Tag>}
      {!def && <Tag color="warning" style={{ fontSize: 10, lineHeight: '16px' }}>未知参数</Tag>}
      {(def?.desc || def?.impact) && (
        <Tooltip
          title={
            <div>
              {def.desc && <div>{def.desc}</div>}
              {def.default !== undefined && <div>默认值：{JSON.stringify(def.default)}</div>}
              {def.impact && <div>生效方式：{def.impact}</div>}
            </div>
          }
        >
          <QuestionCircleOutlined style={{ color: '#999' }} />
        </Tooltip>
      )}
      {def?.doc && (
        <a href={def.doc} target="_blank" rel="noreferrer">
          <LinkOutlined />
        </a>
      )}
    </Space>
  )
}

function AddParam({ schema, existing, onAdd }: { schema?: ParamSchema; existing: Record<string, unknown>; onAdd: (k: string, v: unknown) => void }) {
  const [q, setQ] = useState('')
  const options = useMemo(() => {
    if (!schema || !q) return []
    const s = q.toLowerCase()
    return Object.entries(schema.params)
      .filter(([k, d]) => !(k in existing) && (k.includes(s) || d.label?.includes(q) || d.desc?.toLowerCase().includes(s)))
      .sort(([a], [b]) => Number(!a.startsWith(s)) - Number(!b.startsWith(s)) || a.localeCompare(b))
      .slice(0, 30)
      .map(([k, d]) => ({
        value: k,
        label: (
          <div>
            <span className="mono">{k}</span>
            <Typography.Text type="secondary" style={{ fontSize: 12, marginInlineStart: 6 }}>
              {d.module} · {d.level}
            </Typography.Text>
            {d.desc && (
              <div style={{ fontSize: 12, color: '#888', whiteSpace: 'normal' }}>{d.label ? `${d.label}：` : ''}{d.desc}</div>
            )}
          </div>
        ),
      }))
  }, [schema, q, existing])

  return (
    <AutoComplete
      value={q}
      options={options}
      onSearch={setQ}
      onSelect={(k: string) => {
        onAdd(k, emptyValue(schema?.params[k]))
        setQ('')
      }}
      style={{ width: 320 }}
      popupMatchSelectWidth={480}
    >
      <Input.Search size="small" placeholder="添加参数：输入参数名或说明搜索" enterButton={<PlusOutlined />} />
    </AutoComplete>
  )
}

function HostsCard({ text, model, group, onChange }: { text: string; model: ConfigModel; group: string; onChange: (t: string) => void }) {
  const { message } = App.useApp()
  const [ip, setIp] = useState('')
  const g = model.groups.find((x) => x.name === group)
  if (!g) return null
  const hosts = Object.keys(g.hosts).sort(compareIp)

  const add = () => {
    if (!IPV4.test(ip)) return message.warning('请输入合法的 IPv4 地址')
    if (ip in g.hosts) return message.warning('该主机已在组内')
    onChange(setIn(text, ['all', 'children', group, 'hosts', ip], {}))
    setIp('')
  }

  return (
    <Card
      size="small"
      title={`主机（${hosts.length}）`}
      extra={
        <Space.Compact size="small">
          <Input placeholder="10.10.10.11" value={ip} onChange={(e) => setIp(e.target.value)} onPressEnter={add} style={{ width: 140 }} />
          <Button icon={<PlusOutlined />} onClick={add}>
            添加主机
          </Button>
        </Space.Compact>
      }
    >
      <Space wrap>
        {hosts.map((h) => (
          <Tag
            key={h}
            closable={hosts.length > 1}
            onClose={(e) => {
              e.preventDefault()
              onChange(deleteIn(text, ['all', 'children', group, 'hosts', h]))
            }}
          >
            <span className="mono">{h}</span>
          </Tag>
        ))}
      </Space>
      {g.module === 'PGSQL' && (
        <Typography.Paragraph type="secondary" style={{ fontSize: 12, margin: '8px 0 0' }}>
          这里只修改配置。已运行实例的增删需要通过作业执行（后续里程碑提供），移除实例必须先执行移除、再删除配置。
        </Typography.Paragraph>
      )}
    </Card>
  )
}
