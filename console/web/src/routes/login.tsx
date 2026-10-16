import { useState } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { Alert, Button, Card, Form, Input, Typography } from 'antd'
import { LockOutlined, UserOutlined } from '@ant-design/icons'
import { api } from '@/api/endpoints'
import { ApiError } from '@/api/http'
import { qk } from '@/api/queries'

export const Route = createFileRoute('/login')({
  validateSearch: (search: Record<string, unknown>): { redirect?: string } => ({
    redirect: typeof search.redirect === 'string' ? search.redirect : undefined,
  }),
  component: LoginPage,
})

function LoginPage() {
  const { redirect } = Route.useSearch()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [error, setError] = useState<string>()
  const [loading, setLoading] = useState(false)

  const onFinish = async (v: { username: string; password: string }) => {
    setLoading(true)
    setError(undefined)
    try {
      const me = await api.login(v.username, v.password)
      queryClient.setQueryData(qk.me, me)
      // redirect 只接受站内路径
      const target = redirect?.startsWith('/') && !redirect.startsWith('//') ? redirect : '/'
      await navigate({ to: target })
    } catch (e) {
      setError(e instanceof ApiError && e.status === 401 ? '用户名或密码错误' : String((e as Error).message))
    } finally {
      setLoading(false)
    }
  }

  return (
    <div style={{ minHeight: '100%', display: 'grid', placeItems: 'center', padding: 16 }}>
      <Card style={{ width: 360 }}>
        <Typography.Title level={3} style={{ marginTop: 0 }}>
          Pigsty Console
        </Typography.Title>
        {error && <Alert type="error" title={error} showIcon style={{ marginBottom: 16 }} />}
        <Form layout="vertical" onFinish={onFinish} requiredMark={false}>
          <Form.Item name="username" label="用户名" rules={[{ required: true, message: '请输入用户名' }]}>
            <Input prefix={<UserOutlined />} autoComplete="username" autoFocus />
          </Form.Item>
          <Form.Item name="password" label="密码" rules={[{ required: true, message: '请输入密码' }]}>
            <Input.Password prefix={<LockOutlined />} autoComplete="current-password" />
          </Form.Item>
          <Button type="primary" htmlType="submit" block loading={loading}>
            登录
          </Button>
        </Form>
      </Card>
    </div>
  )
}
