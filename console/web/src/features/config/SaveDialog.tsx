import { lazy, Suspense, useEffect, useMemo, useState } from 'react'
import { Alert, App, Input, Modal, Spin, Table, Tabs, Tag, Typography } from 'antd'
import { useQueryClient } from '@tanstack/react-query'
import { api } from '@/api/endpoints'
import { ApiError } from '@/api/http'
import { qk } from '@/api/queries'
import type { Issue } from '@/api/types'
import { preview, semanticDiff, summarize, type Change } from './semanticDiff'
import { parse } from './yamlPatch'

const DiffView = lazy(() => import('./DiffView'))

interface Props {
  open: boolean
  onClose: () => void
  baseText: string
  baseHash: string
  text: string
  localIssues: Issue[]
  onSaved: () => void
  onRebase: (hash: string) => void
}

const KIND: Record<Change['kind'], { color: string; label: string }> = {
  add: { color: 'green', label: '新增' },
  remove: { color: 'red', label: '删除' },
  change: { color: 'blue', label: '修改' },
}

export function SaveDialog({ open, onClose, baseText, baseHash, text, localIssues, onSaved, onRebase }: Props) {
  const { message } = App.useApp()
  const queryClient = useQueryClient()
  const changes = useMemo(() => {
    const a = parse(baseText)
    const b = parse(text)
    return a.errors.length || b.errors.length ? [] : semanticDiff(a.toJS(), b.toJS())
  }, [baseText, text])
  const [msg, setMsg] = useState('')
  const [saving, setSaving] = useState(false)
  const [serverIssues, setServerIssues] = useState<Issue[]>()
  const [conflict, setConflict] = useState<{ text: string; hash: string }>()

  useEffect(() => {
    if (open) {
      setMsg(summarize(changes))
      setServerIssues(undefined)
      setConflict(undefined)
    }
    // 只在打开时初始化提交说明
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])

  const localErrors = localIssues.filter((i) => i.level === 'error')

  const save = async () => {
    setSaving(true)
    try {
      await api.saveConfig(text, conflict?.hash ?? baseHash, msg.trim() || summarize(changes))
      message.success('已保存')
      await queryClient.invalidateQueries({ queryKey: qk.config })
      await queryClient.invalidateQueries({ queryKey: ['inventory'] })
      onSaved()
    } catch (e) {
      if (e instanceof ApiError && e.status === 409 && e.problem.currentText !== undefined) {
        setConflict({ text: e.problem.currentText, hash: e.problem.currentHash! })
      } else if (e instanceof ApiError && e.status === 422) {
        setServerIssues(e.problem.issues ?? [])
      } else {
        message.error(`保存失败：${(e as Error).message}`)
      }
    } finally {
      setSaving(false)
    }
  }

  const okText = conflict ? '我已核对，基于最新版本保存' : '保存'

  return (
    <Modal
      open={open}
      title="保存配置"
      width="min(1200px, 96vw)"
      okText={okText}
      okButtonProps={{ disabled: localErrors.length > 0, loading: saving, danger: !!conflict }}
      onOk={() => {
        if (conflict) onRebase(conflict.hash)
        save()
      }}
      onCancel={onClose}
      destroyOnHidden
    >
      {localErrors.length > 0 && (
        <Alert type="error" showIcon style={{ marginBottom: 12 }} title={`还有 ${localErrors.length} 个错误，修复后才能保存`} />
      )}
      {serverIssues && (
        <Alert
          type="error"
          showIcon
          style={{ marginBottom: 12 }}
          title="服务端校验未通过"
          description={
            <ul style={{ margin: 0, paddingInlineStart: 18 }}>
              {serverIssues.map((i, n) => (
                <li key={n}>
                  [{i.level}] {i.message}
                  {i.line ? `（第 ${i.line} 行）` : ''}
                </li>
              ))}
            </ul>
          }
        />
      )}
      {conflict && (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 12 }}
          title="配置在你编辑期间被修改过"
          description="下方「行级 diff」左侧是磁盘上的最新版本，右侧是你的草稿。继续保存会用你的草稿覆盖最新版本，请确认没有覆盖掉别人的修改；也可以取消后在源码里手动合并。"
        />
      )}

      <Tabs
        items={[
          {
            key: 'semantic',
            label: `语义变化（${changes.length}）`,
            children: (
              <Table<Change>
                size="small"
                rowKey={(r, i) => `${r.scope}-${r.key}-${i}`}
                dataSource={changes}
                pagination={false}
                scroll={{ y: 360 }}
                locale={{ emptyText: '无语义变化（仅注释或格式）' }}
                columns={[
                  { title: '', dataIndex: 'kind', width: 70, render: (k: Change['kind']) => <Tag color={KIND[k].color}>{KIND[k].label}</Tag> },
                  { title: '位置', dataIndex: 'scope', width: 220 },
                  { title: '参数', dataIndex: 'key', width: 220, render: (k?: string) => k && <span className="mono">{k}</span> },
                  {
                    title: '变化',
                    render: (_, r) => (
                      <span className="mono" style={{ fontSize: 12 }}>
                        {r.before !== undefined && <Typography.Text delete type="secondary">{preview(r.before, r.sensitive)}</Typography.Text>}
                        {r.before !== undefined && r.after !== undefined && ' → '}
                        {r.after !== undefined && <Typography.Text>{preview(r.after, r.sensitive)}</Typography.Text>}
                      </span>
                    ),
                  },
                ]}
              />
            ),
          },
          {
            key: 'lines',
            label: '行级 diff',
            children: (
              <Suspense fallback={<Spin />}>
                <DiffView
                  original={conflict?.text ?? baseText}
                  modified={text}
                  height={400}
                  originalTitle={conflict ? '磁盘上的最新版本' : '已保存版本'}
                  modifiedTitle="我的草稿"
                />
              </Suspense>
            ),
          },
        ]}
      />
      <Input.TextArea
        value={msg}
        onChange={(e) => setMsg(e.target.value)}
        placeholder="提交说明"
        autoSize={{ minRows: 2, maxRows: 4 }}
        style={{ marginTop: 12 }}
      />
    </Modal>
  )
}
