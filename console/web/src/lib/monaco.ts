// Monaco 只在用到编辑器的页面懒加载；使用本地打包的 monaco，不走 CDN（管理节点常常不能上网）
import * as monaco from 'monaco-editor'
import { loader } from '@monaco-editor/react'
import { configureMonacoYaml } from 'monaco-yaml'
import type { JSONSchema } from 'monaco-yaml'
import EditorWorker from './editor.worker?worker'
import YamlWorker from './yaml.worker?worker'

globalThis.MonacoEnvironment = {
  getWorker(_moduleId: string, label: string) {
    return label === 'yaml' ? new YamlWorker() : new EditorWorker()
  },
}

loader.config({ monaco })

export const PIGSTY_MODEL_URI = 'file:///pigsty.yml'

let yaml: ReturnType<typeof configureMonacoYaml> | undefined

export function setYamlSchema(schema: JSONSchema) {
  const options = {
    enableSchemaRequest: false,
    hover: true,
    completion: true,
    validate: true,
    schemas: [{ uri: 'pigsty://schema/pigsty.json', fileMatch: [PIGSTY_MODEL_URI], schema }],
  }
  if (yaml) yaml.update(options)
  else yaml = configureMonacoYaml(monaco, options)
}

export { monaco }
