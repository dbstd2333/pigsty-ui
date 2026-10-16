import { useMemo } from 'react'
import { Tag, Tree } from 'antd'
import type { TreeDataNode } from 'antd'
import { ApartmentOutlined, DesktopOutlined, GlobalOutlined } from '@ant-design/icons'
import { compareIp, MODULE_COLORS, scopeKey, type ConfigModel, type Scope } from './model'

interface Props {
  model: ConfigModel
  scope: Scope
  onSelect: (key: string) => void
}

function hostLabel(ip: string, vars: Record<string, unknown>) {
  const role = vars.pg_role ? String(vars.pg_role) : undefined
  const seq = vars.pg_seq ?? vars.infra_seq ?? vars.etcd_seq ?? vars.minio_seq
  return (
    <span>
      <span className="mono">{ip}</span>
      {role && (
        <Tag color={role === 'primary' ? 'blue' : undefined} style={{ marginInlineStart: 6, fontSize: 11 }}>
          {role}
        </Tag>
      )}
      {!role && seq !== undefined && <span style={{ color: '#999', marginInlineStart: 6 }}>#{String(seq)}</span>}
    </span>
  )
}

export function ConfigTree({ model, scope, onSelect }: Props) {
  const treeData = useMemo<TreeDataNode[]>(
    () => [
      { key: 'global', title: '全局变量', icon: <GlobalOutlined /> },
      ...model.groups.map((g) => ({
        key: `group:${g.name}`,
        icon: <ApartmentOutlined />,
        title: (
          <span>
            {g.name}
            {g.module && (
              <Tag color={MODULE_COLORS[g.module]} style={{ marginInlineStart: 6, fontSize: 11 }}>
                {g.module}
              </Tag>
            )}
          </span>
        ),
        children: Object.keys(g.hosts)
          .sort(compareIp)
          .map((ip) => ({ key: `host:${g.name}:${ip}`, icon: <DesktopOutlined />, title: hostLabel(ip, g.hosts[ip]) })),
      })),
    ],
    [model],
  )

  return (
    <Tree
      showIcon
      blockNode
      defaultExpandAll
      treeData={treeData}
      selectedKeys={[scopeKey(scope)]}
      onSelect={(keys) => keys[0] && onSelect(String(keys[0]))}
    />
  )
}
