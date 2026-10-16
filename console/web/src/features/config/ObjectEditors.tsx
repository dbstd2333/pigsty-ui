/**
 * 复杂参数的结构化编辑器：
 * - ObjectListEditor：对象数组（pg_users、pg_databases、pg_hba_rules、pg_services ……）
 * - ObjectMapEditor：名称 → 对象的映射（pgbackrest_repo、infra_portal ……）
 * 字段来自参数 schema 的 defs；编辑只修改被改动的那一项，其余内容与注释保持原样。
 */
import { useMemo, useState } from 'react'
import { App, Button, Col, Drawer, Form, Input, InputNumber, Popconfirm, Row, Select, Space, Table, Tag, Tooltip, Typography } from 'antd'
import { DeleteOutlined, EditOutlined, PlusOutlined } from '@ant-design/icons'
import { parse as parseYaml, stringify as stringifyYaml } from 'yaml'
import type { ParamDef, ParamSchema } from '@/api/types'
import { isSensitiveKey } from './model'
import { appendItem, deleteIn, deleteItem, setIn, setItem, type Path } from './yamlPatch'

interface PropDef {
  type?: string
  enum?: unknown[]
  items?: { type?: string }
  sensitive?: boolean
  description?: string
}

export interface ObjectDef {
  type?: string
  properties?: Record<string, PropDef>
  required?: string[]
}

type Obj = Record<string, unknown>

/** 参数对应的对象定义：数组项或映射值引用了 defs 时返回 */
export function objectDefOf(def: ParamDef | undefined, schema: ParamSchema | undefined): { mode: 'list' | 'map'; def: ObjectDef } | undefined {
  if (!def || !schema) return undefined
  const ref = (def.items as { $ref?: string } | undefined)?.$ref
  if (!ref) return undefined
  const target = schema.defs[ref.replace('#/defs/', '')] as ObjectDef | undefined
  if (!target?.properties) return undefined
  if (def.type === 'array') return { mode: 'list', def: target }
  if (def.type === 'object') return { mode: 'map', def: target }
  return undefined
}

// 列表里优先展示的字段
const PREFERRED = ['name', 'user', 'db', 'addr', 'auth', 'title', 'port', 'dest', 'selector', 'roles', 'owner', 'domain', 'endpoint', 'type', 'path', 'state', 'order', 'comment']

function summaryKeys(def: ObjectDef, rows: Obj[]): string[] {
  const props = Object.keys(def.properties ?? {})
  const used = new Set(rows.flatMap((r) => Object.keys(r)))
  return PREFERRED.filter((k) => props.includes(k) && used.has(k)).slice(0, 5)
}

function show(v: unknown, sensitive: boolean) {
  if (v === undefined) return null
  if (sensitive) return '******'
  if (Array.isArray(v)) return v.map((x) => <Tag key={String(x)} style={{ marginInlineEnd: 2 }}>{String(x)}</Tag>)
  if (typeof v === 'object' && v !== null) return <span className="mono">{JSON.stringify(v)}</span>
  if (typeof v === 'boolean') return v ? '是' : '否'
  return String(v)
}

// ---------------------------------------------------------------------------

interface ListProps {
  text: string
  path: Path
  value: Obj[]
  def: ObjectDef
  title: string
  onChange: (text: string) => void
}

export function ObjectListEditor({ text, path, value, def, title, onChange }: ListProps) {
  const { message } = App.useApp()
  const [editing, setEditing] = useState<{ index: number; value: Obj } | null>(null)
  const keys = summaryKeys(def, value)

  const apply = (fn: () => string) => {
    try {
      onChange(fn())
      return true
    } catch (e) {
      message.error(`修改失败：${(e as Error).message}`)
      return false
    }
  }

  return (
    <>
      <Table<Obj & { __i: number }>
        size="small"
        rowKey="__i"
        pagination={false}
        dataSource={value.map((v, i) => ({ ...v, __i: i }))}
        columns={[
          ...keys.map((k) => ({
            title: k,
            dataIndex: k,
            render: (v: unknown) => show(v, !!def.properties?.[k]?.sensitive || isSensitiveKey(k)),
          })),
          {
            title: '',
            width: 80,
            align: 'right' as const,
            render: (_: unknown, r: Obj & { __i: number }) => (
              <Space size={0}>
                <Button type="text" size="small" icon={<EditOutlined />} onClick={() => setEditing({ index: r.__i, value: value[r.__i] })} />
                <Popconfirm title="删除这一项？" onConfirm={() => apply(() => deleteItem(text, path, r.__i))}>
                  <Button type="text" size="small" danger icon={<DeleteOutlined />} />
                </Popconfirm>
              </Space>
            ),
          },
        ]}
        footer={() => (
          <Button size="small" type="dashed" icon={<PlusOutlined />} onClick={() => setEditing({ index: -1, value: {} })}>
            添加
          </Button>
        )}
      />
      <ObjectDrawer
        open={!!editing}
        title={editing?.index === -1 ? `新增 ${title} 项` : `编辑 ${title}[${editing?.index}]`}
        def={def}
        value={editing?.value ?? {}}
        onClose={() => setEditing(null)}
        onSubmit={(obj) => {
          const ok = apply(() => (editing!.index === -1 ? appendItem(text, path, obj) : setItem(text, path, editing!.index, obj)))
          if (ok) setEditing(null)
        }}
      />
    </>
  )
}

// ---------------------------------------------------------------------------

interface MapProps {
  text: string
  path: Path
  value: Record<string, Obj>
  def: ObjectDef
  title: string
  onChange: (text: string) => void
}

export function ObjectMapEditor({ text, path, value, def, title, onChange }: MapProps) {
  const { message } = App.useApp()
  const [editing, setEditing] = useState<{ name: string; isNew: boolean; value: Obj } | null>(null)
  const rows = Object.entries(value ?? {}).map(([name, v]) => ({ __name: name, ...(v ?? {}) }))
  const keys = summaryKeys(def, rows)

  const apply = (fn: () => string) => {
    try {
      onChange(fn())
      return true
    } catch (e) {
      message.error(`修改失败：${(e as Error).message}`)
      return false
    }
  }

  return (
    <>
      <Table<Obj & { __name: string }>
        size="small"
        rowKey="__name"
        pagination={false}
        dataSource={rows}
        columns={[
          { title: '名称', dataIndex: '__name', render: (n: string) => <span className="mono">{n}</span> },
          ...keys.map((k) => ({
            title: k,
            dataIndex: k,
            render: (v: unknown) => show(v, !!def.properties?.[k]?.sensitive || isSensitiveKey(k)),
          })),
          {
            title: '',
            width: 80,
            align: 'right' as const,
            render: (_: unknown, r: Obj & { __name: string }) => (
              <Space size={0}>
                <Button
                  type="text"
                  size="small"
                  icon={<EditOutlined />}
                  onClick={() => setEditing({ name: r.__name, isNew: false, value: value[r.__name] ?? {} })}
                />
                <Popconfirm title={`删除 ${r.__name}？`} onConfirm={() => apply(() => deleteIn(text, [...path, r.__name]))}>
                  <Button type="text" size="small" danger icon={<DeleteOutlined />} />
                </Popconfirm>
              </Space>
            ),
          },
        ]}
        footer={() => (
          <Button size="small" type="dashed" icon={<PlusOutlined />} onClick={() => setEditing({ name: '', isNew: true, value: {} })}>
            添加
          </Button>
        )}
      />
      <ObjectDrawer
        open={!!editing}
        title={editing?.isNew ? `新增 ${title} 项` : `编辑 ${title}.${editing?.name}`}
        def={def}
        value={editing?.value ?? {}}
        withName={editing?.isNew ? Object.keys(value ?? {}) : undefined}
        onClose={() => setEditing(null)}
        onSubmit={(obj, name) => {
          const key = editing!.isNew ? name! : editing!.name
          if (apply(() => setIn(text, [...path, key], obj))) setEditing(null)
        }}
      />
    </>
  )
}

// ---------------------------------------------------------------------------

interface DrawerProps {
  open: boolean
  title: string
  def: ObjectDef
  value: Obj
  withName?: string[] // 新增映射项时需要填写名称；值为已存在的名称
  onClose: () => void
  onSubmit: (value: Obj, name?: string) => void
}

const YAML_FIELD = (t?: string) => t === 'object'

function ObjectDrawer({ open, title, def, value, withName, onClose, onSubmit }: DrawerProps) {
  const [form] = Form.useForm()
  const props = def.properties ?? {}
  const required = new Set(def.required ?? (props.name ? ['name'] : []))
  const order = useMemo(
    () =>
      Object.keys(props).sort(
        (a, b) =>
          Number(!required.has(a)) - Number(!required.has(b)) ||
          Number(!PREFERRED.includes(a)) - Number(!PREFERRED.includes(b)) ||
          PREFERRED.indexOf(a) - PREFERRED.indexOf(b) ||
          a.localeCompare(b),
      ),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [def],
  )
  const extra = Object.keys(value).filter((k) => !(k in props))

  // 对象字段用 YAML 文本编辑，其余直接用原值
  const initial = useMemo(() => {
    const v: Obj = {}
    for (const [k, x] of Object.entries(value)) v[k] = YAML_FIELD(props[k]?.type) && x !== undefined ? stringifyYaml(x).trimEnd() : x
    if (extra.length) v.__extra = stringifyYaml(Object.fromEntries(extra.map((k) => [k, value[k]]))).trimEnd()
    return v
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value, def])

  const submit = async () => {
    const raw = (await form.validateFields()) as Obj
    const out: Obj = {}
    // 保留原有字段顺序，新字段按表单顺序追加
    const keys = [...Object.keys(value).filter((k) => k in props), ...order.filter((k) => !(k in value))]
    for (const k of keys) {
      let v = raw[k]
      if (v === undefined || v === null || v === '' || (Array.isArray(v) && v.length === 0 && value[k] === undefined)) continue
      if (YAML_FIELD(props[k]?.type) && typeof v === 'string') v = parseYaml(v)
      out[k] = v
    }
    if (typeof raw.__extra === 'string' && raw.__extra.trim()) Object.assign(out, parseYaml(raw.__extra) as Obj)
    onSubmit(out, raw.__name as string | undefined)
  }

  return (
    <Drawer
      open={open}
      title={title}
      size="large"
      onClose={onClose}
      destroyOnHidden
      extra={
        <Space>
          <Button onClick={onClose}>取消</Button>
          <Button type="primary" onClick={submit}>
            确定
          </Button>
        </Space>
      }
    >
      <Form form={form} layout="vertical" initialValues={initial} key={JSON.stringify(initial)} size="small">
        <Row gutter={12}>
          {withName && (
            <Col span={24}>
              <Form.Item
                name="__name"
                label="名称"
                rules={[
                  { required: true, message: '请输入名称' },
                  { pattern: /^[A-Za-z0-9_.-]+$/, message: '只能包含字母、数字、_ . -' },
                  { validator: (_, v) => (withName.includes(v) ? Promise.reject(new Error('名称已存在')) : Promise.resolve()) },
                ]}
              >
                <Input />
              </Form.Item>
            </Col>
          )}
          {order.map((k) => (
            <Col span={YAML_FIELD(props[k].type) || props[k].type === 'array' ? 24 : 12} key={k}>
              <Form.Item
                name={k}
                label={
                  <Tooltip title={props[k].description}>
                    <span className="mono">{k}</span>
                  </Tooltip>
                }
                rules={required.has(k) ? [{ required: true, message: `请填写 ${k}` }] : undefined}
              >
                <FieldInput name={k} prop={props[k]} />
              </Form.Item>
            </Col>
          ))}
          {extra.length > 0 && (
            <Col span={24}>
              <Form.Item name="__extra" label="其他字段（YAML）" extra="schema 中没有定义的字段，原样保留">
                <Input.TextArea className="mono" autoSize={{ minRows: 2, maxRows: 8 }} />
              </Form.Item>
            </Col>
          )}
        </Row>
      </Form>
      <Typography.Paragraph type="secondary" style={{ fontSize: 12 }}>
        留空的字段不会写入配置，使用 Pigsty 默认值。
      </Typography.Paragraph>
    </Drawer>
  )
}

interface FieldProps {
  name: string
  prop: PropDef
  value?: unknown
  onChange?: (v: unknown) => void
}

function FieldInput({ name, prop, value, onChange }: FieldProps) {
  if (prop.enum) {
    return (
      <Select allowClear value={value} onChange={onChange} options={prop.enum.map((x) => ({ value: x as string, label: String(x) }))} />
    )
  }
  switch (prop.type) {
    case 'boolean':
      return (
        <Select
          allowClear
          value={value as boolean | undefined}
          onChange={onChange}
          options={[
            { value: true, label: '是' },
            { value: false, label: '否' },
          ]}
        />
      )
    case 'integer':
    case 'number':
      return <InputNumber value={value as number | undefined} onChange={onChange} precision={prop.type === 'integer' ? 0 : undefined} style={{ width: '100%' }} />
    case 'array':
      return (
        <Select
          mode="tags"
          open={false}
          tokenSeparators={[',', ' ']}
          value={(value as unknown[] | undefined)?.map(String) ?? []}
          onChange={(v: string[]) => onChange?.(prop.items?.type === 'integer' ? v.map(Number) : v)}
        />
      )
    case 'object':
      return <Input.TextArea className="mono" autoSize={{ minRows: 2, maxRows: 8 }} value={value as string} onChange={(e) => onChange?.(e.target.value)} placeholder="YAML" />
    default:
      return prop.sensitive || isSensitiveKey(name) ? (
        <Input.Password value={value as string} onChange={(e) => onChange?.(e.target.value)} autoComplete="new-password" />
      ) : (
        <Input value={value as string} onChange={(e) => onChange?.(e.target.value)} />
      )
  }
}
