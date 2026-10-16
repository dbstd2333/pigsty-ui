import type { JSONSchema } from 'monaco-yaml'
import type { ParamDef, ParamSchema } from '@/api/types'

// 把参数 schema 转成 monaco-yaml 用的 JSON Schema，只做补全、悬停文档和类型提示，
// 语义规则由 rules.ts 负责
function paramToJson(def: ParamDef): JSONSchema {
  const s: JSONSchema = {}
  if (def.type && def.type !== 'any') s.type = def.type === 'integer' ? ['integer', 'string'] : def.type
  if (def.enum) s.enum = def.enum as JSONSchema['enum']
  if (def.$ref) s.$ref = def.$ref.replace('#/defs/', '#/definitions/')
  if (def.items) s.items = (JSON.parse(JSON.stringify(def.items).replaceAll('#/defs/', '#/definitions/')) as JSONSchema)
  const lines = [def.label, def.desc, def.default !== undefined ? `默认值：${JSON.stringify(def.default)}` : '', `${def.module} · ${def.level}${def.impact ? ' · ' + def.impact : ''}`]
  s.description = lines.filter(Boolean).join('\n\n')
  if (def.default !== undefined) s.default = def.default as JSONSchema['default']
  return s
}

export function toJsonSchema(schema: ParamSchema): JSONSchema {
  const vars: JSONSchema = {
    type: 'object',
    properties: Object.fromEntries(Object.entries(schema.params).map(([k, d]) => [k, paramToJson(d)])),
  }
  const group: JSONSchema = {
    type: 'object',
    properties: {
      hosts: { type: 'object', description: '组内主机：IP → 主机变量', additionalProperties: { anyOf: [vars, { type: 'null' }] } },
      vars,
    },
  }
  const defs = JSON.parse(JSON.stringify(schema.defs ?? {}).replaceAll('#/defs/', '#/definitions/')) as Record<string, JSONSchema>
  return {
    type: 'object',
    definitions: defs,
    properties: {
      all: {
        type: 'object',
        properties: {
          children: { type: 'object', description: '集群与模块分组', additionalProperties: group },
          vars,
        },
      },
    },
  }
}
