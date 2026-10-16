import { Table, Tag, Typography } from 'antd'
import type { InventoryModule } from '@/api/types'
import { allHosts, MODULE_COLORS, type ConfigModel } from './model'

const MODULES: InventoryModule[] = ['INFRA', 'ETCD', 'MINIO', 'PGSQL', 'REDIS', 'APP']

interface Cell {
  group: string
  detail?: string
}

function detail(module: InventoryModule, vars: Record<string, unknown>): string | undefined {
  switch (module) {
    case 'PGSQL':
      return [vars.pg_role, vars.pg_seq !== undefined ? `#${vars.pg_seq}` : ''].filter(Boolean).join(' ')
    case 'INFRA':
      return vars.infra_seq !== undefined ? `#${vars.infra_seq}` : undefined
    case 'ETCD':
      return vars.etcd_seq !== undefined ? `#${vars.etcd_seq}` : undefined
    case 'MINIO':
      return vars.minio_seq !== undefined ? `#${vars.minio_seq}` : undefined
    default:
      return undefined
  }
}

export function MatrixView({ model, onSelectHost }: { model: ConfigModel; onSelectHost: (group: string, host: string) => void }) {
  const hosts = allHosts(model)
  const used = MODULES.filter((m) => model.groups.some((g) => g.module === m))
  const cells = (ip: string, m: InventoryModule): Cell[] =>
    model.groups.filter((g) => g.module === m && ip in g.hosts).map((g) => ({ group: g.name, detail: detail(m, g.hosts[ip]) }))
  const other = (ip: string) => model.groups.filter((g) => !g.module && ip in g.hosts).map((g) => g.name)

  return (
    <Table
      size="small"
      rowKey={(r) => r}
      dataSource={hosts}
      pagination={false}
      scroll={{ x: 'max-content' }}
      columns={[
        { title: '节点', fixed: 'left', render: (ip: string) => <span className="mono">{ip}</span> },
        ...used.map((m) => ({
          title: <Tag color={MODULE_COLORS[m]}>{m}</Tag>,
          render: (ip: string) =>
            cells(ip, m).map((c) => (
              <div key={c.group}>
                <Typography.Link onClick={() => onSelectHost(c.group, ip)}>{c.group}</Typography.Link>
                {c.detail && (
                  <Typography.Text type="secondary" style={{ fontSize: 12, marginInlineStart: 4 }}>
                    {c.detail}
                  </Typography.Text>
                )}
              </div>
            )),
        })),
        {
          title: '其他组',
          render: (ip: string) => other(ip).map((g) => <Tag key={g}>{g}</Tag>),
        },
      ]}
    />
  )
}
