import { useMutation, useQueryClient } from '@tanstack/react-query'
import { App, Button, Form, InputNumber, Modal, Space, Typography } from 'antd'
import { api } from '@/api/endpoints'
import { qk } from '@/api/queries'
import type { NodeSpec } from '@/api/types'

export function SpecDialog({ ip, spec, onClose }: { ip: string; spec?: NodeSpec; onClose: () => void }) {
  const { message } = App.useApp()
  const queryClient = useQueryClient()
  const [form] = Form.useForm<{ cpu: number; memGb: number; diskGb: number }>()
  const save = useMutation({
    mutationFn: (v: { cpu: number; memGb: number; diskGb: number }) => api.saveNodeSpec(ip, { cpu: v.cpu, memMb: Math.round(v.memGb * 1024), diskGb: v.diskGb }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: qk.nodeSpecs })
      message.success('已保存')
      onClose()
    },
    onError: (e) => message.error(`保存失败：${(e as Error).message}`),
  })
  const del = useMutation({
    mutationFn: () => api.deleteNodeSpec(ip),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: qk.nodeSpecs })
      onClose()
    },
  })
  return (
    <Modal
      open
      title={`规划规格：${ip}`}
      onCancel={onClose}
      onOk={() => form.submit()}
      confirmLoading={save.isPending}
      footer={(orig) => (
        <Space>
          {spec && (
            <Button danger onClick={() => del.mutate()} loading={del.isPending}>
              清除
            </Button>
          )}
          {orig}
        </Space>
      )}
    >
      <Typography.Paragraph type="secondary">
        用于还没有监控数据的节点做容量规划，只保存在 console 本地，不写入 pigsty.yml。节点纳管后会改用实际指标。
      </Typography.Paragraph>
      <Form
        form={form}
        layout="inline"
        initialValues={{ cpu: spec?.cpu ?? 4, memGb: spec ? spec.memMb / 1024 : 16, diskGb: spec?.diskGb ?? 200 }}
        onFinish={(v) => save.mutate(v)}
      >
        <Form.Item name="cpu" label="CPU 核数" rules={[{ required: true }]}>
          <InputNumber min={1} max={512} />
        </Form.Item>
        <Form.Item name="memGb" label="内存 GB" rules={[{ required: true }]}>
          <InputNumber min={0.5} max={8192} step={1} />
        </Form.Item>
        <Form.Item name="diskGb" label="数据盘 GB" rules={[{ required: true }]}>
          <InputNumber min={1} max={1_000_000} />
        </Form.Item>
      </Form>
    </Modal>
  )
}
