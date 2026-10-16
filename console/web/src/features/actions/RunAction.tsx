import { useEffect, useMemo, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import {
  Alert,
  App,
  Button,
  Checkbox,
  Descriptions,
  Form,
  Input,
  InputNumber,
  Modal,
  Select,
  Space,
  Spin,
  Switch,
  Tag,
  Typography,
} from 'antd'
import type { ButtonProps } from 'antd'
import { CheckCircleFilled, CloseCircleFilled, ExclamationCircleFilled, PlayCircleOutlined } from '@ant-design/icons'
import dayjs from 'dayjs'
import { api } from '@/api/endpoints'
import { ApiError } from '@/api/http'
import { useActions } from '@/api/queries'
import type { ActionDef, ActionParam, ActionPlan, Risk } from '@/api/types'
import { compareIp, type ConfigModel } from '@/features/config/model'
import { useSavedModel } from '@/features/config/useSavedModel'

export const RISK_META: Record<Risk, { color: string; label: string }> = {
  readonly: { color: 'default', label: '只读' },
  low: { color: 'green', label: '低风险' },
  medium: { color: 'gold', label: '中风险' },
  high: { color: 'orange', label: '高风险' },
  critical: { color: 'red', label: '最高风险' },
}

export function RiskTag({ risk }: { risk: Risk }) {
  const m = RISK_META[risk] ?? RISK_META.low
  return <Tag color={m.color}>{m.label}</Tag>
}

type Params = Record<string, unknown>

interface ActionButtonProps extends Omit<ButtonProps, 'onClick'> {
  action: string
  params?: Params
  /** 预填参数是否锁定（默认锁定，只能修改未预填的参数） */
  lockParams?: boolean
}

/** 一个按钮 + 执行对话框。对话框内：填参数 → 生成计划 → 确认 → 提交作业 → 跳转作业详情 */
export function ActionButton({ action, params, lockParams = true, children, ...rest }: ActionButtonProps) {
  const [open, setOpen] = useState(false)
  return (
    <>
      <Button icon={<PlayCircleOutlined />} {...rest} onClick={() => setOpen(true)}>
        {children}
      </Button>
      {open && <RunActionDialog action={action} params={params} lockParams={lockParams} onClose={() => setOpen(false)} />}
    </>
  )
}

interface DialogProps {
  action: string
  params?: Params
  lockParams?: boolean
  onClose: () => void
}

export function RunActionDialog({ action, params: initial = {}, lockParams = true, onClose }: DialogProps) {
  const actions = useActions()
  const def = actions.data?.find((a) => a.id === action)
  const { model } = useSavedModel()
  const { message } = App.useApp()
  const navigate = useNavigate()
  const queryClient = useQueryClient()

  const [params, setParams] = useState<Params>(() => ({ ...initial }))
  const [check, setCheck] = useState(false)
  const [plan, setPlan] = useState<ActionPlan>()
  const [planError, setPlanError] = useState<string[]>()
  const [confirmText, setConfirmText] = useState('')
  const [boxes, setBoxes] = useState<string[]>([])

  // 填入参数默认值
  useEffect(() => {
    if (!def) return
    setParams((p) => {
      const next = { ...p }
      for (const ap of def.params) if (next[ap.name] === undefined && ap.default !== null && ap.default !== undefined) next[ap.name] = ap.default
      return next
    })
  }, [def])

  const planMut = useMutation({
    mutationFn: () => api.planAction(action, params, check),
    onSuccess: (p) => {
      setPlan(p)
      setPlanError(undefined)
      setConfirmText('')
      setBoxes([])
    },
    onError: (e) => {
      setPlan(undefined)
      if (e instanceof ApiError && e.problem.issues?.length) setPlanError(e.problem.issues.map((i) => `${i.path ? i.path + '：' : ''}${i.message}`))
      else setPlanError([(e as Error).message])
    },
  })

  const submit = useMutation({
    mutationFn: () =>
      api.submitJob({
        action,
        params,
        check,
        confirm: plan?.gate ? { text: confirmText, checkboxes: boxes } : undefined,
      }),
    onSuccess: (job) => {
      queryClient.invalidateQueries({ queryKey: ['jobs'] })
      message.success('作业已提交')
      onClose()
      navigate({ to: '/jobs/$id', params: { id: job.id } })
    },
    onError: (e) => {
      if (e instanceof ApiError && e.status === 409 && e.problem.runningJobId) {
        const id = e.problem.runningJobId
        Modal.warning({
          title: '已有变更作业在运行',
          content: `同一时间只允许一个变更作业。正在运行：${id}`,
          okText: '查看该作业',
          onOk: () => {
            onClose()
            navigate({ to: '/jobs/$id', params: { id } })
          },
        })
      } else if (e instanceof ApiError && e.problem.issues?.length) {
        setPlanError(e.problem.issues.map((i) => i.message))
      } else message.error(`提交失败：${(e as Error).message}`)
    },
  })

  // 参数变化后计划失效
  const setParam = (k: string, v: unknown) => {
    setParams((p) => ({ ...p, [k]: v }))
    setPlan(undefined)
  }

  const blocked = plan?.preconditions.some((p) => !p.ok && p.level === 'error')
  const gateOk = !plan?.gate || (confirmText === plan.gate.confirmText && plan.gate.checkboxes.every((b) => boxes.includes(b)))
  const risk = plan?.risk ?? def?.risk ?? 'low'

  return (
    <Modal
      open
      width={720}
      title={
        <Space>
          {def?.title ?? action}
          {def && <RiskTag risk={risk} />}
        </Space>
      }
      onCancel={onClose}
      footer={
        <Space>
          <Button onClick={onClose}>取消</Button>
          <Button onClick={() => planMut.mutate()} loading={planMut.isPending} disabled={!def}>
            {plan ? '重新生成计划' : '生成执行计划'}
          </Button>
          <Button
            type="primary"
            danger={risk === 'high' || risk === 'critical'}
            disabled={!plan || blocked || !gateOk}
            loading={submit.isPending}
            onClick={() => submit.mutate()}
          >
            {check ? '提交预演' : '执行'}
          </Button>
        </Space>
      }
    >
      {actions.isLoading ? (
        <Spin />
      ) : !def ? (
        <Alert type="error" title={`后端未提供动作 ${action}`} />
      ) : (
        <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr)', gap: 12 }}>
          <Typography.Text type="secondary">{def.description}</Typography.Text>
          <ParamForm def={def} params={params} locked={lockParams ? Object.keys(initial) : []} model={model} onChange={setParam} />
          {def.check && (
            <Space>
              <Switch size="small" checked={check} onChange={(v) => (setCheck(v), setPlan(undefined))} />
              <span>预演（--check，不做实际修改）</span>
            </Space>
          )}
          {planError && <Alert type="error" showIcon title="参数校验失败" description={<ul style={{ margin: 0, paddingInlineStart: 18 }}>{planError.map((m) => <li key={m}>{m}</li>)}</ul>} />}
          {plan && <PlanView plan={plan} />}
          {plan?.gate && (
            <GateForm gate={plan.gate} text={confirmText} boxes={boxes} onText={setConfirmText} onBoxes={setBoxes} />
          )}
        </div>
      )}
    </Modal>
  )
}

function ParamForm({
  def,
  params,
  locked,
  model,
  onChange,
}: {
  def: ActionDef
  params: Params
  locked: string[]
  model?: ConfigModel
  onChange: (k: string, v: unknown) => void
}) {
  if (def.params.length === 0) return null
  return (
    <Form layout="horizontal" labelCol={{ flex: '120px' }} size="small" style={{ marginBottom: 0 }}>
      {def.params.map((p) => (
        <Form.Item key={p.name} label={p.label} required={p.required} help={p.help ?? undefined} style={{ marginBottom: 10 }}>
          <ParamInput p={p} params={params} disabled={locked.includes(p.name)} model={model} onChange={(v) => onChange(p.name, v)} />
        </Form.Item>
      ))}
    </Form>
  )
}

/** 根据参数类型从当前（已保存）配置给出候选值 */
export function paramOptions(p: ActionParam, params: Params, model?: ConfigModel): string[] {
  if (!model) return []
  const of = p.of ? String(params[p.of] ?? '') : ''
  const group = model.groups.find((g) => g.name === of)
  const listNames = (key: string) =>
    Array.isArray(group?.vars[key]) ? (group!.vars[key] as { name?: string }[]).map((x) => String(x?.name ?? '')).filter(Boolean) : []
  switch (p.type) {
    case 'pg_cluster':
      return model.groups.filter((g) => g.module === 'PGSQL').map((g) => g.name)
    case 'redis_cluster':
      return model.groups.filter((g) => g.module === 'REDIS').map((g) => g.name)
    case 'pg_member':
    case 'ip_list':
      return group ? Object.keys(group.hosts).sort(compareIp) : [...new Set(model.groups.flatMap((g) => Object.keys(g.hosts)))].sort(compareIp)
    case 'pg_user':
      return listNames('pg_users')
    case 'pg_db':
      return listNames('pg_databases')
    case 'enum':
      return p.enum
    default:
      return []
  }
}

function ParamInput({
  p,
  params,
  disabled,
  model,
  onChange,
}: {
  p: ActionParam
  params: Params
  disabled: boolean
  model?: ConfigModel
  onChange: (v: unknown) => void
}) {
  const v = params[p.name]
  const opts = useMemo(() => paramOptions(p, params, model), [p, params, model])
  switch (p.type) {
    case 'bool':
      return <Switch checked={!!v} disabled={disabled} onChange={onChange} />
    case 'int':
      return <InputNumber value={v as number} disabled={disabled} onChange={onChange} />
    case 'ip_list':
      return (
        <Select
          mode="tags"
          value={(v as string[]) ?? []}
          disabled={disabled}
          onChange={onChange}
          options={opts.map((o) => ({ value: o, label: <span className="mono">{o}</span> }))}
          placeholder="选择或输入 IP"
        />
      )
    case 'object':
      return (
        <Input.TextArea
          className="mono"
          disabled={disabled}
          rows={3}
          defaultValue={v === undefined ? '' : JSON.stringify(v)}
          placeholder='JSON，如 {"max_connections": "500"}'
          onBlur={(e) => {
            try {
              onChange(e.target.value.trim() ? JSON.parse(e.target.value) : undefined)
            } catch {
              // 非法 JSON 时保留原值，由计划校验报错
            }
          }}
        />
      )
    case 'string':
    case 'datetime':
      return <Input value={(v as string) ?? ''} disabled={disabled} onChange={(e) => onChange(e.target.value)} />
    default:
      return (
        <Select
          showSearch
          value={v as string}
          disabled={disabled}
          onChange={onChange}
          options={opts.map((o) => ({ value: o, label: o }))}
          placeholder={p.of && !params[p.of] ? `先选择${p.of}` : '请选择'}
        />
      )
  }
}

export function PlanView({ plan }: { plan: ActionPlan | { commands: string[]; preconditions: ActionPlan['preconditions']; targets?: string[] } }) {
  return (
    <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr)', gap: 8 }}>
      {plan.preconditions.length > 0 && (
        <div>
          {plan.preconditions.map((c) => (
            <div key={c.id} style={{ display: 'flex', gap: 6, alignItems: 'baseline' }}>
              {c.ok ? (
                <CheckCircleFilled style={{ color: '#52c41a' }} />
              ) : c.level === 'error' ? (
                <CloseCircleFilled style={{ color: '#ff4d4f' }} />
              ) : (
                <ExclamationCircleFilled style={{ color: '#faad14' }} />
              )}
              <span>{c.message}</span>
            </div>
          ))}
        </div>
      )}
      {'targets' in plan && plan.targets && plan.targets.length > 0 && (
        <Descriptions size="small" column={1} items={[{ label: '目标主机', children: plan.targets.map((t) => <Tag key={t} className="mono">{t}</Tag>) }]} />
      )}
      <div>
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          将执行的命令（在 Pigsty 目录下按顺序执行，任一步失败即停止）
        </Typography.Text>
        <pre className="mono" style={{ background: '#1f1f1f', color: '#e6e6e6', padding: '8px 10px', borderRadius: 6, margin: '4px 0 0', whiteSpace: 'pre-wrap', wordBreak: 'break-all', fontSize: 12 }}>
          {plan.commands.map((c) => `$ ${c}`).join('\n')}
        </pre>
      </div>
    </div>
  )
}

export function GateForm({
  gate,
  text,
  boxes,
  onText,
  onBoxes,
}: {
  gate: NonNullable<ActionPlan['gate']>
  text: string
  boxes: string[]
  onText: (t: string) => void
  onBoxes: (b: string[]) => void
}) {
  const age = gate.lastBackupAt ? dayjs().diff(dayjs(gate.lastBackupAt), 'hour', true) : null
  return (
    <Alert
      type="error"
      showIcon
      title="高风险操作确认"
      description={
        <div style={{ display: 'grid', gap: 8, marginTop: 4 }}>
          <div>
            最近一次成功备份：
            {gate.lastBackupAt ? (
              <Typography.Text type={age! > gate.backupMaxAgeHours ? 'danger' : undefined}>
                {dayjs(gate.lastBackupAt).format('YYYY-MM-DD HH:mm')}（{dayjs(gate.lastBackupAt).fromNow()}）
                {age! > gate.backupMaxAgeHours && `，超过 ${gate.backupMaxAgeHours} 小时`}
              </Typography.Text>
            ) : (
              <Typography.Text type="danger">未知</Typography.Text>
            )}
          </div>
          {gate.checkboxes.length > 0 && (
            <Checkbox.Group value={boxes} onChange={(v) => onBoxes(v as string[])} style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr)', gap: 4 }}>
              {gate.checkboxes.map((b) => (
                <Checkbox key={b} value={b}>
                  {b}
                </Checkbox>
              ))}
            </Checkbox.Group>
          )}
          <div>
            请输入 <Typography.Text code copyable>{gate.confirmText}</Typography.Text> 以确认：
            <Input value={text} onChange={(e) => onText(e.target.value)} status={text && text !== gate.confirmText ? 'error' : undefined} style={{ marginTop: 4 }} />
          </div>
        </div>
      }
    />
  )
}
