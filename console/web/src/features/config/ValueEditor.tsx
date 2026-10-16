import { useEffect, useState } from 'react'
import { Input, InputNumber, Select, Switch } from 'antd'
import type { ParamDef } from '@/api/types'

export type EditorKind = 'boolean' | 'integer' | 'number' | 'enum' | 'string' | 'secret' | 'tags' | 'complex'

const isObj = (v: unknown) => typeof v === 'object' && v !== null && !Array.isArray(v)

export function editorKind(def: ParamDef | undefined, value: unknown, sensitive: boolean): EditorKind {
  if (def?.enum?.length && (value === undefined || typeof value !== 'object')) return 'enum'
  const t = def?.type
  if (t === 'boolean' || typeof value === 'boolean') return 'boolean'
  if (t === 'integer' || Number.isInteger(value)) return 'integer'
  if (t === 'number' || typeof value === 'number') return 'number'
  if (Array.isArray(value)) return value.every((x) => typeof x !== 'object' || x === null) ? 'tags' : 'complex'
  if (t === 'array') return value === undefined ? 'tags' : 'complex'
  if (t === 'object' || isObj(value)) return 'complex'
  return sensitive ? 'secret' : 'string'
}

export function emptyValue(def: ParamDef | undefined): unknown {
  if (def?.default !== undefined) return def.default
  switch (def?.type) {
    case 'boolean':
      return false
    case 'integer':
    case 'number':
      return 0
    case 'array':
      return []
    case 'object':
      return {}
    default:
      return ''
  }
}

// 输入框失焦或回车时才提交，避免每个按键都生成一次补丁
function TextInput({ value, onCommit, secret }: { value: string; onCommit: (v: string) => void; secret?: boolean }) {
  const [local, setLocal] = useState(value)
  useEffect(() => setLocal(value), [value])
  const commit = () => local !== value && onCommit(local)
  const C = secret ? Input.Password : Input
  return <C size="small" value={local} onChange={(e) => setLocal(e.target.value)} onBlur={commit} onPressEnter={commit} />
}

interface Props {
  kind: EditorKind
  def?: ParamDef
  value: unknown
  onCommit: (v: unknown) => void
}

export function ValueEditor({ kind, def, value, onCommit }: Props) {
  switch (kind) {
    case 'boolean':
      return <Switch size="small" checked={value === true} onChange={onCommit} />
    case 'integer':
    case 'number':
      return (
        <InputNumber
          size="small"
          value={typeof value === 'number' ? value : undefined}
          precision={kind === 'integer' ? 0 : undefined}
          onChange={(v) => v !== null && onCommit(v)}
          style={{ width: 160 }}
        />
      )
    case 'enum':
      return (
        <Select
          size="small"
          value={value as string}
          onChange={onCommit}
          options={(def?.enum ?? []).map((x) => ({ value: x as string, label: String(x) }))}
          style={{ minWidth: 160 }}
        />
      )
    case 'tags':
      return (
        <Select
          size="small"
          mode="tags"
          value={(Array.isArray(value) ? value : []).map(String)}
          // 保留数字类型（如端口列表）
          onChange={(vals: string[]) => onCommit(vals.map((s) => (/^-?\d+$/.test(s) ? Number(s) : s)))}
          tokenSeparators={[',', ' ']}
          style={{ width: '100%' }}
          open={false}
        />
      )
    case 'secret':
    case 'string':
      return <TextInput value={value == null ? '' : String(value)} onCommit={onCommit} secret={kind === 'secret'} />
    default:
      return null
  }
}
