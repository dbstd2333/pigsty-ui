import { createFileRoute } from '@tanstack/react-router'
import { useEffect } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { App, Button, Card, Descriptions, Form, Input, InputNumber, Popconfirm, Space, Typography } from 'antd'
import { api } from '@/api/endpoints'
import { ApiError } from '@/api/http'
import { qk, useMeta, useSettings } from '@/api/queries'
import type { Settings } from '@/api/types'
import { BASELINE, type BaselineId } from '@/features/memory/baseline'

export const Route = createFileRoute('/_app/settings')({
  component: SettingsPage,
})

function SettingsPage() {
  const { message } = App.useApp()
  const meta = useMeta()
  const [form] = Form.useForm()

  const onFinish = async (v: { oldPassword: string; newPassword: string }) => {
    try {
      await api.changePassword(v.oldPassword, v.newPassword)
      message.success('密码已修改')
      form.resetFields()
    } catch (e) {
      message.error(e instanceof ApiError && e.status === 401 ? '原密码不正确' : (e as Error).message)
    }
  }

  return (
    <div style={{ display: 'grid', gap: 16, maxWidth: 720 }}>
      <Card title="修改密码">
        <Form form={form} layout="vertical" onFinish={onFinish} style={{ maxWidth: 360 }}>
          <Form.Item name="oldPassword" label="原密码" rules={[{ required: true }]}>
            <Input.Password autoComplete="current-password" />
          </Form.Item>
          <Form.Item name="newPassword" label="新密码" rules={[{ required: true, min: 8, message: '至少 8 位' }]}>
            <Input.Password autoComplete="new-password" />
          </Form.Item>
          <Form.Item
            name="confirm"
            label="确认新密码"
            dependencies={['newPassword']}
            rules={[
              { required: true },
              ({ getFieldValue }) => ({
                validator: (_, v) =>
                  v === getFieldValue('newPassword') ? Promise.resolve() : Promise.reject(new Error('两次输入不一致')),
              }),
            ]}
          >
            <Input.Password autoComplete="new-password" />
          </Form.Item>
          <Button type="primary" htmlType="submit">
            保存
          </Button>
        </Form>
      </Card>
      <SystemSettings />
      <Card title="运行信息">
        <Descriptions column={1} size="small">
          <Descriptions.Item label="Console 版本">{meta.data?.version}</Descriptions.Item>
          <Descriptions.Item label="Pigsty 版本">{meta.data?.pigstyVersion || '-'}</Descriptions.Item>
          <Descriptions.Item label="Pigsty 目录">
            <span className="mono">{meta.data?.pigstyHome}</span>
          </Descriptions.Item>
          <Descriptions.Item label="有效值计算">
            {meta.data?.inventoryEngine === 'ansible' ? 'ansible-inventory' : '内置（本机无 ansible）'}
          </Descriptions.Item>
          <Descriptions.Item label="bin/validate">{meta.data?.validateAvailable ? '可用' : '不可用'}</Descriptions.Item>
          <Descriptions.Item label="运行模式">{meta.data?.simulate ? '模拟（作业与状态为假数据）' : '正常'}</Descriptions.Item>
        </Descriptions>
      </Card>
    </div>
  )
}

type SettingsForm = Pick<Settings, 'victoriaUrl' | 'alertmanagerUrl' | 'grafanaUrl' | 'backupMaxAgeHours' | 'ansibleForks'>

function SystemSettings() {
  const settings = useSettings()
  const queryClient = useQueryClient()
  const { message } = App.useApp()
  const [form] = Form.useForm<SettingsForm>()
  const save = useMutation({
    mutationFn: (v: Partial<Settings>) => api.saveSettings(v),
    onSuccess: (s) => {
      queryClient.setQueryData(qk.settings, s)
      queryClient.invalidateQueries({ queryKey: ['status'] })
      message.success('已保存')
    },
    onError: (e) => message.error(`保存失败：${(e as Error).message}`),
  })
  useEffect(() => {
    if (settings.data) form.setFieldsValue(settings.data)
  }, [settings.data, form])
  const d = settings.data?.derived
  const overrides = Object.entries(settings.data?.memoryBaseline ?? {})

  return (
    <Card title="系统设置" loading={settings.isLoading}>
      <Form form={form} layout="vertical" onFinish={(v) => save.mutate(v)} style={{ maxWidth: 520 }}>
        <Form.Item name="victoriaUrl" label="VictoriaMetrics 地址" extra={`留空自动推导：${d?.victoriaUrl || '（推导失败）'}`}>
          <Input placeholder={d?.victoriaUrl} className="mono" />
        </Form.Item>
        <Form.Item name="alertmanagerUrl" label="Alertmanager 地址" extra={`留空自动推导：${d?.alertmanagerUrl || '（推导失败）'}`}>
          <Input placeholder={d?.alertmanagerUrl} className="mono" />
        </Form.Item>
        <Form.Item name="grafanaUrl" label="Grafana 地址（用于跳转监控面板）" extra={`留空自动推导：${d?.grafanaUrl || '（推导失败）'}`}>
          <Input placeholder={d?.grafanaUrl} className="mono" />
        </Form.Item>
        <Space size={24}>
          <Form.Item name="backupMaxAgeHours" label="备份新鲜度阈值（小时）" tooltip="高风险操作前，最近成功备份超过该时长会阻断">
            <InputNumber min={1} max={24 * 30} />
          </Form.Item>
          <Form.Item name="ansibleForks" label="Ansible 并发（forks）" tooltip="1c1g 管理节点建议 5">
            <InputNumber min={1} max={100} />
          </Form.Item>
        </Space>
        <Form.Item style={{ marginBottom: 0 }}>
          <Button type="primary" htmlType="submit" loading={save.isPending}>
            保存
          </Button>
        </Form.Item>
      </Form>
      {overrides.length > 0 && (
        <div style={{ marginTop: 16 }}>
          <Typography.Text strong>内存经验值的本地覆盖</Typography.Text>
          <div style={{ margin: '6px 0' }}>
            {overrides.map(([k, v]) => (
              <div key={k} style={{ fontSize: 13 }}>
                {BASELINE[k as BaselineId]?.label ?? k}：{v} MB（默认 {BASELINE[k as BaselineId]?.mb ?? '-'} MB）
              </div>
            ))}
          </div>
          <Popconfirm title="清除所有覆盖，恢复默认经验值？" onConfirm={() => save.mutate({ memoryBaseline: {} })}>
            <Button size="small">恢复默认</Button>
          </Popconfirm>
        </div>
      )}
    </Card>
  )
}
