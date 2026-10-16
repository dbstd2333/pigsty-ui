import { useMemo, useState } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import {
  Alert,
  App,
  Button,
  Card,
  Checkbox,
  DatePicker,
  Descriptions,
  Input,
  Modal,
  Radio,
  Select,
  Space,
  Spin,
  Steps,
  Tag,
  Typography,
} from 'antd'
import dayjs, { type Dayjs } from 'dayjs'
import utc from 'dayjs/plugin/utc'
import { api } from '@/api/endpoints'
import { ApiError } from '@/api/http'
import { useBackups, useConfig, useStatusCluster } from '@/api/queries'
import type { PitrAction, PitrPlan, PitrTarget, PitrTargetType } from '@/api/types'
import { ActionButton, GateForm, PlanView } from '@/features/actions/RunAction'
import { IPV4 } from '@/features/clusters/templates'
import { compareIp } from '@/features/config/model'
import { editDraft } from '@/features/config/useConfigDraft'
import { useSavedModel } from '@/features/config/useSavedModel'
import { setIn } from '@/features/config/yamlPatch'
import { fmtBytes } from '@/lib/format'

dayjs.extend(utc)

interface Search {
  time?: string
  set?: string
  restorePoint?: string
}

export const Route = createFileRoute('/_app/clusters/$cls/pitr')({
  validateSearch: (s: Record<string, unknown>): Search => ({
    time: typeof s.time === 'string' ? s.time : undefined,
    set: typeof s.set === 'string' ? s.set : undefined,
    restorePoint: typeof s.restorePoint === 'string' ? s.restorePoint : undefined,
  }),
  component: PitrWizard,
})

const TARGET_TYPES: { value: PitrTargetType; label: string }[] = [
  { value: 'time', label: '时间点' },
  { value: 'name', label: '恢复点' },
  { value: 'immediate', label: '备份集（恢复到备份结束时）' },
  { value: 'latest', label: '最新（回放全部 WAL）' },
  { value: 'lsn', label: 'LSN（高级）' },
  { value: 'xid', label: '事务 ID（高级）' },
]

function PitrWizard() {
  const { cls } = Route.useParams()
  const search = Route.useSearch()
  const backups = useBackups(cls)
  const status = useStatusCluster(cls)
  const { model } = useSavedModel()
  const config = useConfig()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const { message } = App.useApp()

  const [step, setStep] = useState(0)
  const [type, setType] = useState<PitrTargetType>(search.restorePoint ? 'name' : search.set ? 'immediate' : search.time ? 'time' : 'time')
  const [time, setTime] = useState<Dayjs | null>(search.time ? dayjs(search.time) : null)
  const [name, setName] = useState(search.restorePoint ?? '')
  const [set, setSet] = useState(search.set ?? '')
  const [lsn, setLsn] = useState('')
  const [xid, setXid] = useState('')
  const [exclusive, setExclusive] = useState(false)
  const [mode, setMode] = useState<'clone' | 'inplace'>('clone')
  const [newCluster, setNewCluster] = useState(`${cls}-pitr`)
  const [cloneIps, setCloneIps] = useState<string[]>([])
  const [action, setAction] = useState<PitrAction>('pause')
  const [backupData, setBackupData] = useState(true)
  const [plan, setPlan] = useState<PitrPlan>()
  const [planErr, setPlanErr] = useState<string>()
  const [confirmText, setConfirmText] = useState('')
  const [boxes, setBoxes] = useState<string[]>([])

  const info = backups.data
  const target: PitrTarget = useMemo(() => {
    const t: PitrTarget = { type }
    if (type === 'time' && time) t.time = time.toISOString()
    if (type === 'name') t.name = name
    if (type === 'lsn') t.lsn = lsn
    if (type === 'xid') t.xid = xid
    if (set) t.set = set
    if (exclusive && (type === 'time' || type === 'lsn' || type === 'xid')) t.exclusive = true
    return t
  }, [type, time, name, lsn, xid, set, exclusive])

  const targetOk =
    (type === 'time' && !!time) || (type === 'name' && !!name) || (type === 'immediate' && !!set) || type === 'latest' || (type === 'lsn' && !!lsn) || (type === 'xid' && !!xid)
  const inWindow = !info?.window || type !== 'time' || !time || (time.isAfter(dayjs(info.window.start)) && time.isBefore(dayjs(info.window.end).add(1, 'minute')))
  const newGroup = model?.groups.find((g) => g.name === newCluster)
  const srcGroup = model?.groups.find((g) => g.name === cls)

  const planMut = useMutation({
    mutationFn: () => api.pitrPlan(cls, { mode, target, action, backup: mode === 'inplace' && backupData, newCluster: mode === 'clone' ? newCluster : undefined }),
    onSuccess: (p) => {
      setPlan(p)
      setPlanErr(undefined)
      setConfirmText('')
      setBoxes([])
    },
    onError: (e) => {
      setPlan(undefined)
      setPlanErr(e instanceof ApiError && e.problem.issues?.length ? e.problem.issues.map((i) => i.message).join('；') : (e as Error).message)
    },
  })

  const submit = useMutation({
    mutationFn: () =>
      api.submitJob({
        action: mode === 'clone' ? 'pitr-clone' : 'pitr-inplace',
        params: mode === 'clone' ? { cluster: newCluster, source: cls, target, action } : { cluster: cls, target, action, backup: backupData },
        check: false,
        confirm: plan?.gate ? { text: confirmText, checkboxes: boxes } : undefined,
      }),
    onSuccess: (job) => {
      queryClient.invalidateQueries({ queryKey: ['jobs'] })
      navigate({ to: '/jobs/$id', params: { id: job.id } })
    },
    onError: (e) => {
      if (e instanceof ApiError && e.status === 409) Modal.warning({ title: '无法提交', content: e.message })
      else message.error(`提交失败：${(e as Error).message}`)
    },
  })

  // 恢复到新集群：生成新集群配置（复制源集群的变量，换集群名与节点）
  const writeCloneConfig = () => {
    if (!srcGroup || !config.data) return
    const vars = { ...srcGroup.vars, pg_cluster: newCluster }
    delete (vars as Record<string, unknown>).pg_vip_enabled
    delete (vars as Record<string, unknown>).pg_vip_address
    const hosts = Object.fromEntries(cloneIps.map((ip, i) => [ip, { pg_seq: i + 1, pg_role: i === 0 ? 'primary' : 'replica' }]))
    editDraft(config.data, (t) => setIn(t, ['all', 'children', newCluster], { hosts, vars }))
    navigate({ to: '/config', search: { scope: `group:${newCluster}` } })
  }

  const blocked = plan?.preconditions.some((p) => !p.ok && p.level === 'error')
  const gateOk = !plan?.gate || (confirmText === plan.gate.confirmText && plan.gate.checkboxes.every((b) => boxes.includes(b)))
  const cloneReady = mode === 'inplace' || !!newGroup

  if (backups.isLoading) return <Spin style={{ display: 'block', marginTop: 80 }} />

  return (
    <div style={{ display: 'grid', gap: 12 }}>
      <Typography.Title level={4} style={{ margin: 0 }}>
        <Link to="/clusters/$cls" params={{ cls }}>
          {cls}
        </Link>{' '}
        / <Link to="/clusters/$cls/backup" params={{ cls }}>备份与恢复</Link> / 时间点恢复
      </Typography.Title>
      <Card size="small">
        <Steps
          size="small"
          current={step}
          onChange={(s) => s < step && setStep(s)}
          style={{ marginBottom: 16 }}
          items={[{ title: '恢复目标' }, { title: '恢复方式' }, { title: '恢复后动作' }, { title: '确认并执行' }]}
        />

        {step === 0 && (
          <div style={{ display: 'grid', gap: 12, maxWidth: 760 }}>
            {info?.window ? (
              <Alert
                type="info"
                showIcon
                title={`可恢复窗口：${dayjs(info.window.start).format('YYYY-MM-DD HH:mm:ss')} → ${dayjs(info.window.end).format('YYYY-MM-DD HH:mm:ss')}（本地时间）`}
              />
            ) : (
              <Alert type="warning" showIcon title="没有可用的可恢复窗口（可能没有成功的备份，或无法读取备份信息）" />
            )}
            <Radio.Group value={type} onChange={(e) => setType(e.target.value)} options={TARGET_TYPES} />
            {type === 'time' && (
              <Space direction="vertical">
                <DatePicker
                  showTime
                  value={time}
                  onChange={setTime}
                  format="YYYY-MM-DD HH:mm:ss"
                  disabledDate={(d) => !!info?.window && (d.isBefore(dayjs(info.window.start), 'day') || d.isAfter(dayjs(info.window.end), 'day'))}
                  style={{ width: 260 }}
                />
                {time && (
                  <Typography.Text type="secondary" className="mono" style={{ fontSize: 12 }}>
                    本地 {time.format('YYYY-MM-DD HH:mm:ss Z')} ｜ UTC {time.utc().format('YYYY-MM-DD HH:mm:ss')}+00（提交给 pg_pitr 的值）
                  </Typography.Text>
                )}
                {!inWindow && <Typography.Text type="danger">目标时间不在可恢复窗口内</Typography.Text>}
              </Space>
            )}
            {type === 'name' && (
              <Select
                showSearch
                style={{ width: 360 }}
                value={name || undefined}
                onChange={setName}
                placeholder="选择恢复点"
                options={(info?.restorePoints ?? []).map((r) => ({ value: r.name, label: `${r.name}（${dayjs(r.time).format('MM-DD HH:mm:ss')}）` }))}
              />
            )}
            {(type === 'lsn' || type === 'xid') && (
              <Input
                className="mono"
                style={{ width: 360 }}
                placeholder={type === 'lsn' ? '0/7C82CB8' : '250000'}
                value={type === 'lsn' ? lsn : xid}
                onChange={(e) => (type === 'lsn' ? setLsn : setXid)(e.target.value.trim())}
              />
            )}
            {(type === 'immediate' || type === 'time' || type === 'name') && (
              <Space>
                <span>指定备份集：</span>
                <Select
                  allowClear
                  style={{ width: 320 }}
                  value={set || undefined}
                  onChange={(v) => setSet(v ?? '')}
                  placeholder={type === 'immediate' ? '必选' : '自动（目标之前最近的一个）'}
                  options={[...(info?.backups ?? [])]
                    .filter((b) => !b.error)
                    .reverse()
                    .map((b) => ({ value: b.label, label: `${b.label}（${b.type}，${dayjs(b.stop).format('MM-DD HH:mm')}）` }))}
                />
              </Space>
            )}
            {(type === 'time' || type === 'lsn' || type === 'xid') && (
              <Checkbox checked={exclusive} onChange={(e) => setExclusive(e.target.checked)}>
                不包含目标本身（exclusive，恢复到目标之前）
              </Checkbox>
            )}
          </div>
        )}

        {step === 1 && (
          <div style={{ display: 'grid', gap: 12, maxWidth: 760 }}>
            <Radio.Group value={mode} onChange={(e) => setMode(e.target.value)}>
              <Space direction="vertical">
                <Radio value="clone">
                  <b>恢复到新集群（推荐）</b>
                  <Typography.Text type="secondary" style={{ marginInlineStart: 8 }}>
                    不影响源集群，验证数据后再决定如何处理
                  </Typography.Text>
                </Radio>
                <Radio value="inplace">
                  <b>原地恢复</b>
                  <Typography.Text type="danger" style={{ marginInlineStart: 8 }}>
                    覆盖当前集群，目标时间之后的数据会丢失
                  </Typography.Text>
                </Radio>
              </Space>
            </Radio.Group>
            {mode === 'clone' && (
              <Card size="small" type="inner" title="新集群">
                <Space direction="vertical" style={{ width: '100%' }}>
                  <Space>
                    <span>集群名：</span>
                    <Input className="mono" value={newCluster} onChange={(e) => setNewCluster(e.target.value.trim())} style={{ width: 240 }} />
                    {newGroup ? <Tag color="success">已在配置中（{Object.keys(newGroup.hosts).length} 台）</Tag> : <Tag color="warning">配置中还没有</Tag>}
                  </Space>
                  {info?.repo.method === 'local' && (
                    <Alert
                      type="warning"
                      showIcon
                      title="当前使用本地备份仓库（pgbackrest_method: local）"
                      description="备份在源主库的本地磁盘上，其他节点读不到。新集群只能放在源主库所在节点（使用不同端口和数据目录），或先把备份仓库改为 MinIO / S3。"
                    />
                  )}
                  {!newGroup && (
                    <>
                      <Typography.Text>选择新集群的节点（第一台为主库），生成配置后在工作台核对、保存，然后回到这里：</Typography.Text>
                      <Select
                        mode="tags"
                        style={{ width: '100%' }}
                        value={cloneIps}
                        onChange={(v: string[]) => setCloneIps(v.filter((x) => IPV4.test(x)))}
                        options={[...new Set(model?.groups.flatMap((g) => Object.keys(g.hosts)) ?? [])].sort(compareIp).map((ip) => ({ value: ip, label: ip }))}
                        placeholder="选择或输入 IP"
                      />
                      <Button disabled={!cloneIps.length || !srcGroup} onClick={writeCloneConfig}>
                        生成新集群配置（复制 {cls} 的变量）并去工作台保存
                      </Button>
                    </>
                  )}
                  {newGroup && (
                    <Space>
                      <Typography.Text type="secondary">新集群需要先初始化（如已初始化可跳过）：</Typography.Text>
                      <ActionButton size="small" action="pgsql-create" params={{ cluster: newCluster }}>
                        初始化 {newCluster}
                      </ActionButton>
                    </Space>
                  )}
                </Space>
              </Card>
            )}
            {mode === 'inplace' && (
              <Card size="small" type="inner">
                <Space direction="vertical">
                  <Checkbox checked={backupData} onChange={(e) => setBackupData(e.target.checked)}>
                    恢复前把当前数据目录拷贝到 /pg/data-backup（backup: true，强烈建议）
                  </Checkbox>
                  <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                    当前成员：{(status.data?.members ?? []).map((m) => `${m.name}(${m.role})`).join('，') || '未知'}。恢复期间整个集群不可用，其他变更作业会被锁定。
                  </Typography.Text>
                </Space>
              </Card>
            )}
          </div>
        )}

        {step === 2 && (
          <Radio.Group value={action} onChange={(e) => setAction(e.target.value)}>
            <Space direction="vertical">
              <Radio value="pause">
                <b>暂停（默认，推荐）</b>
                <Typography.Text type="secondary" style={{ marginInlineStart: 8 }}>
                  恢复到目标后暂停回放，验证数据无误后再点「确认提升」
                </Typography.Text>
              </Radio>
              <Radio value="promote">
                <b>直接提升</b>
                <Typography.Text type="secondary" style={{ marginInlineStart: 8 }}>
                  恢复完成后立即提升为可写主库
                </Typography.Text>
              </Radio>
              <Radio value="shutdown">
                <b>关闭</b>
                <Typography.Text type="secondary" style={{ marginInlineStart: 8 }}>
                  恢复完成后关闭实例
                </Typography.Text>
              </Radio>
            </Space>
          </Radio.Group>
        )}

        {step === 3 && (
          <div style={{ display: 'grid', gap: 12, maxWidth: 860 }}>
            <Descriptions
              size="small"
              bordered
              column={1}
              items={[
                { label: '恢复目标', children: describeTarget(target) },
                { label: '恢复方式', children: mode === 'clone' ? `恢复到新集群 ${newCluster}` : <Typography.Text type="danger">原地恢复 {cls}</Typography.Text> },
                { label: '恢复后动作', children: action },
                ...(plan
                  ? [
                      { label: '使用备份集', children: <span className="mono">{plan.backupSet ?? '自动'}</span> },
                      {
                        label: '将丢失的数据',
                        children: plan.lossWindow ? (
                          <Typography.Text type="danger">
                            {dayjs(plan.lossWindow.from).format('YYYY-MM-DD HH:mm:ss')} → {dayjs(plan.lossWindow.to).format('YYYY-MM-DD HH:mm:ss')}
                          </Typography.Text>
                        ) : (
                          '无（不覆盖源集群）'
                        ),
                      },
                      { label: '预计耗时', children: plan.estimatedSeconds ? `约 ${Math.ceil(plan.estimatedSeconds / 60)} 分钟（估算）` : '-' },
                      ...(info ? [{ label: '最近备份大小', children: fmtBytes(info.backups.filter((b) => !b.error).at(-1)?.size) }] : []),
                    ]
                  : []),
              ]}
            />
            <Space>
              <Button onClick={() => planMut.mutate()} loading={planMut.isPending} disabled={!cloneReady}>
                {plan ? '重新生成计划' : '生成恢复计划'}
              </Button>
              {!cloneReady && <Typography.Text type="warning">请先在上一步生成并保存新集群配置</Typography.Text>}
            </Space>
            {planErr && <Alert type="error" showIcon title={planErr} />}
            {plan && (
              <>
                {!plan.inWindow && <Alert type="error" showIcon title="恢复目标不在可恢复窗口内" />}
                <PlanView plan={plan} />
                <details>
                  <summary style={{ cursor: 'pointer', color: '#888' }}>pg_pitr 参数</summary>
                  <pre className="mono" style={{ fontSize: 12 }}>{JSON.stringify(plan.pgPitr, null, 2)}</pre>
                </details>
                {plan.gate && <GateForm gate={plan.gate} text={confirmText} boxes={boxes} onText={setConfirmText} onBoxes={setBoxes} />}
                <Space>
                  <Button
                    type="primary"
                    danger={mode === 'inplace'}
                    disabled={blocked || !gateOk || !plan.inWindow}
                    loading={submit.isPending}
                    onClick={() => submit.mutate()}
                  >
                    {mode === 'inplace' ? '执行原地恢复' : '开始恢复到新集群'}
                  </Button>
                  <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                    执行分 down → pitr → up 三个阶段，可在作业页查看逐主机进度
                  </Typography.Text>
                </Space>
              </>
            )}
          </div>
        )}

        {step < 3 && (
          <Space style={{ marginTop: 16 }}>
            {step > 0 && <Button onClick={() => setStep(step - 1)}>上一步</Button>}
            <Button
              type="primary"
              disabled={(step === 0 && (!targetOk || !inWindow)) || (step === 1 && !cloneReady)}
              onClick={() => (setPlan(undefined), setStep(step + 1))}
            >
              下一步
            </Button>
          </Space>
        )}
      </Card>
      <Card size="small" title="说明">
        <Typography.Paragraph type="secondary" style={{ margin: 0, fontSize: 13 }}>
          暂停模式下恢复完成后，请自己用只读连接核对关键表的行数和最近写入时间（console 不读取业务数据），确认无误后在作业页点「确认提升」。
          提升后建议立即发起一次全量备份，为新时间线建立基线；原地恢复时 /pg/data-backup 会占用与数据目录相同的磁盘空间，确认无误后手动清理。
        </Typography.Paragraph>
      </Card>
    </div>
  )
}

function describeTarget(t: PitrTarget) {
  switch (t.type) {
    case 'time':
      return t.time ? `${dayjs(t.time).format('YYYY-MM-DD HH:mm:ss')}（UTC ${dayjs(t.time).utc().format('YYYY-MM-DD HH:mm:ss')}+00）${t.exclusive ? '，不含目标' : ''}` : '-'
    case 'name':
      return `恢复点 ${t.name}`
    case 'immediate':
      return `备份集 ${t.set} 结束时`
    case 'latest':
      return '最新（回放全部 WAL）'
    case 'lsn':
      return `LSN ${t.lsn}`
    case 'xid':
      return `事务 ${t.xid}`
  }
}
