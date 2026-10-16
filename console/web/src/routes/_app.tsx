import { Link, Outlet, createFileRoute, redirect, useNavigate, useRouterState } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { Button, Dropdown, Layout, Menu, Space, Tag, Typography } from 'antd'
import type { MenuProps } from 'antd'
import {
  AppstoreOutlined,
  CloudServerOutlined,
  DashboardOutlined,
  DatabaseOutlined,
  FileTextOutlined,
  HistoryOutlined,
  PlayCircleOutlined,
  SettingOutlined,
  UserOutlined,
} from '@ant-design/icons'
import { api } from '@/api/endpoints'
import { ApiError } from '@/api/http'
import { qk, useMeta } from '@/api/queries'

export const Route = createFileRoute('/_app')({
  beforeLoad: async ({ context, location }) => {
    try {
      const me = await context.queryClient.ensureQueryData({ queryKey: qk.me, queryFn: api.me })
      return { me }
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) {
        throw redirect({ to: '/login', search: { redirect: location.href } })
      }
      throw e
    }
  },
  component: AppLayout,
})

const menuItems: MenuProps['items'] = [
  { key: '/', icon: <DashboardOutlined />, label: <Link to="/">概览</Link> },
  {
    key: 'config',
    icon: <FileTextOutlined />,
    label: '配置',
    children: [
      { key: '/config', icon: <AppstoreOutlined />, label: <Link to="/config">配置工作台</Link> },
      { key: '/config/history', icon: <HistoryOutlined />, label: <Link to="/config/history">配置历史</Link> },
    ],
  },
  { key: '/clusters', icon: <DatabaseOutlined />, label: <Link to="/clusters">集群</Link> },
  {
    key: 'infra',
    icon: <CloudServerOutlined />,
    label: '节点与容量',
    children: [
      { key: '/nodes', label: <Link to="/nodes">节点</Link> },
      { key: '/capacity', label: <Link to="/capacity">容量预估</Link> },
    ],
  },
  { key: '/jobs', icon: <PlayCircleOutlined />, label: <Link to="/jobs">作业</Link> },
  { key: '/settings', icon: <SettingOutlined />, label: <Link to="/settings">设置</Link> },
]

// 详情页高亮所属的一级菜单
function selectedKey(pathname: string) {
  const p = pathname.replace(/\/$/, '') || '/'
  if (p.startsWith('/clusters')) return '/clusters'
  if (p.startsWith('/jobs')) return '/jobs'
  return p
}

function AppLayout() {
  const { me } = Route.useRouteContext()
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const meta = useMeta()

  const logout = async () => {
    await api.logout().catch(() => undefined)
    queryClient.clear()
    navigate({ to: '/login', search: {} })
  }

  return (
    <Layout style={{ minHeight: '100vh' }}>
      <Layout.Sider width={208} theme="light" style={{ borderRight: '1px solid #eef0f3', position: 'sticky', top: 0, height: '100vh', overflow: 'auto' }}>
        <div style={{ padding: '18px 20px 12px' }}>
          <Typography.Text strong style={{ fontSize: 16 }}>
            Pigsty Console
          </Typography.Text>
          {meta.data?.pigstyVersion && (
            <div>
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                Pigsty {meta.data.pigstyVersion}
              </Typography.Text>
            </div>
          )}
        </div>
        <Menu
          mode="inline"
          selectedKeys={[selectedKey(pathname)]}
          defaultOpenKeys={['config', 'infra']}
          items={menuItems}
          style={{ borderInlineEnd: 0 }}
        />
      </Layout.Sider>
      <Layout style={{ minWidth: 0 }}>
        <Layout.Header
          style={{
            background: '#fff',
            height: 48,
            lineHeight: '48px',
            padding: '0 20px',
            display: 'flex',
            justifyContent: 'space-between',
            alignItems: 'center',
            borderBottom: '1px solid #eef0f3',
          }}
        >
          <Space>
            {meta.data?.simulate && (
              <Tag color="magenta" title="后端以 --simulate 启动：作业不会真正执行，状态与备份为模拟数据">
                模拟模式
              </Tag>
            )}
          </Space>
          <Dropdown
            menu={{
              items: [
                { key: 'settings', label: <Link to="/settings">修改密码</Link> },
                { key: 'logout', label: '退出登录', onClick: logout },
              ],
            }}
          >
            <Button type="text" icon={<UserOutlined />}>
              {me.username}
            </Button>
          </Dropdown>
        </Layout.Header>
        <Layout.Content style={{ padding: 16, minWidth: 0 }}>
          <Outlet />
        </Layout.Content>
      </Layout>
    </Layout>
  )
}
