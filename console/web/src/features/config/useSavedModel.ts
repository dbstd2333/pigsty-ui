import { useMemo } from 'react'
import { useConfig } from '@/api/queries'
import { buildModel, type ConfigModel } from './model'
import { parse } from './yamlPatch'

/** 已保存配置（服务端版本）的模型，供集群、节点、容量等只读页面使用 */
export function useSavedModel() {
  const config = useConfig()
  const model = useMemo<ConfigModel | undefined>(() => {
    if (!config.data) return undefined
    const doc = parse(config.data.text)
    return doc.errors.length ? undefined : buildModel(doc.toJS())
  }, [config.data])
  return { config, model }
}
