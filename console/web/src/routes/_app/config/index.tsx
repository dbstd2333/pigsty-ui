import { lazy, Suspense, useMemo, useState } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { Alert, App, Badge, Button, Card, Popconfirm, Result, Segmented, Space, Spin, Tabs, Tag, Typography } from 'antd'
import { SaveOutlined, UndoOutlined } from '@ant-design/icons'
import { useMutation } from '@tanstack/react-query'
import { api } from '@/api/endpoints'
import { useSchema } from '@/api/queries'
import type { Issue } from '@/api/types'
import { ConfigTree } from '@/features/config/ConfigTree'
import { EffectiveView } from '@/features/config/EffectiveView'
import { FormView } from '@/features/config/FormView'
import { IssuesPanel } from '@/features/config/IssuesPanel'
import { MatrixView } from '@/features/config/MatrixView'
import { parseScopeKey, scopeKey, type Scope } from '@/features/config/model'
import { runRules } from '@/features/config/rules'
import { SaveDialog } from '@/features/config/SaveDialog'
import { useConfigDraft } from '@/features/config/useConfigDraft'
import { buildModel } from '@/features/config/model'
import { lineOf, parse, type Path } from '@/features/config/yamlPatch'
import { MemoryPanel, memoryIssues, useEstimates } from '@/features/memory/MemoryPanel'
import { useSpecsMap } from '@/features/memory/useSpecs'

const SourceView = lazy(() => import('@/features/config/SourceView'))

type View = 'form' | 'source' | 'effective' | 'matrix'

interface Search {
  view?: View
  scope?: string
  host?: string
}

export const Route = createFileRoute('/_app/config/')({
  validateSearch: (s: Record<string, unknown>): Search => ({
    view: ['form', 'source', 'effective', 'matrix'].includes(s.view as string) ? (s.view as View) : undefined,
    scope: typeof s.scope === 'string' ? s.scope : undefined,
    host: typeof s.host === 'string' ? s.host : undefined,
  }),
  component: Workbench,
})

const PANEL_HEIGHT = 'calc(100vh - 170px)'

function Workbench() {
  const search = Route.useSearch()
  const navigate = useNavigate({ from: Route.fullPath })
  const { message } = App.useApp()
  const draft = useConfigDraft()
  const schema = useSchema()
  const [saveOpen, setSaveOpen] = useState(false)
  const [reveal, setReveal] = useState<{ line: number; nonce: number }>()
  const [serverIssues, setServerIssues] = useState<{ text: string; issues: Issue[] }>()

  const view: View = search.view ?? 'form'
  const scope: Scope = parseScopeKey(search.scope ?? 'global')
  const setSearch = (patch: Partial<Search>) => navigate({ search: (prev) => ({ ...prev, ...patch }), replace: true })

  const { specs, baseline } = useSpecsMap()
  const savedModel = useMemo(() => {
    if (!draft.base) return undefined
    const d = parse(draft.base.text)
    return d.errors.length ? undefined : buildModel(d.toJS())
  }, [draft.base])
  const estimates = useEstimates(draft.doc.errors.length ? undefined : draft.model, schema.data, specs, baseline)
  const savedEstimates = useEstimates(savedModel, schema.data, specs, baseline)
  const [side, setSide] = useState<'issues' | 'memory'>('issues')

  const localIssues = useMemo(
    () => [...runRules(draft.text, draft.doc, draft.model, schema.data), ...memoryIssues(estimates)],
    [draft.text, draft.doc, draft.model, schema.data, estimates],
  )
  // 服务端结果只在文本未变时有效
  const issues = useMemo(
    () => (serverIssues && serverIssues.text === draft.text ? [...localIssues, ...serverIssues.issues] : localIssues),
    [localIssues, serverIssues, draft.text],
  )

  const serverCheck = useMutation({
    mutationFn: () => api.validateConfig(draft.text),
    onSuccess: (r) => setServerIssues({ text: draft.text, issues: r.issues }),
    onError: (e) => message.error(`服务端校验失败：${(e as Error).message}`),
  })

  const jumpLine = (line: number) => {
    setSearch({ view: 'source' })
    setReveal({ line, nonce: Date.now() })
  }
  const jumpPath = (path: Path) => {
    const line = lineOf(draft.text, draft.doc, path)
    if (line) jumpLine(line)
  }

  if (draft.server.isLoading) return <Spin style={{ display: 'block', marginTop: 80 }} />
  if (draft.server.error || !draft.base)
    return <Result status="error" title="读取配置失败" subTitle={(draft.server.error as Error | null)?.message} />

  const errorCount = issues.filter((i) => i.level === 'error').length

  return (
    <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr)', gap: 12 }}>
      <Space style={{ justifyContent: 'space-between', width: '100%' }} wrap>
        <Space>
          <Typography.Title level={4} style={{ margin: 0 }}>
            配置工作台
          </Typography.Title>
          {draft.base.commit && (
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              当前版本 <span className="mono">{draft.base.commit.shortHash}</span> · {draft.base.commit.author}
            </Typography.Text>
          )}
          {draft.dirty && <Tag color="orange">有未保存的修改</Tag>}
        </Space>
        <Space>
          <Popconfirm title="放弃所有未保存的修改？" onConfirm={draft.discard} disabled={!draft.dirty}>
            <Button icon={<UndoOutlined />} disabled={!draft.dirty}>
              放弃修改
            </Button>
          </Popconfirm>
          <Badge count={errorCount} size="small">
            <Button type="primary" icon={<SaveOutlined />} disabled={!draft.dirty} onClick={() => setSaveOpen(true)}>
              保存…
            </Button>
          </Badge>
        </Space>
      </Space>

      {draft.stale && (
        <Alert
          type="warning"
          showIcon
          title="配置文件在你编辑期间被修改过（其他人或手工修改）。保存时会让你核对差异。"
        />
      )}

      <div style={{ display: 'grid', gridTemplateColumns: '240px minmax(0, 1fr) 300px', gap: 12, alignItems: 'start' }}>
        <Card size="small" styles={{ body: { padding: 8, height: PANEL_HEIGHT, overflow: 'auto' } }}>
          <ConfigTree
            model={draft.model}
            scope={scope}
            onSelect={(k) => setSearch({ scope: k, view: view === 'matrix' || view === 'source' ? 'form' : view, host: parseScopeKey(k).kind === 'host' ? (parseScopeKey(k) as { host: string }).host : search.host })}
          />
        </Card>

        <Card size="small" styles={{ body: { paddingTop: 0, height: PANEL_HEIGHT, overflow: 'auto' } }}>
          <Tabs
            activeKey={view}
            onChange={(k) => setSearch({ view: k as View })}
            items={[
              {
                key: 'form',
                label: '表单',
                children: draft.doc.errors.length ? (
                  <Alert type="error" showIcon title="YAML 有语法错误，请先在源码视图中修复" />
                ) : (
                  <FormView
                    text={draft.text}
                    model={draft.model}
                    scope={scope}
                    schema={schema.data}
                    onChange={draft.setText}
                    onJump={jumpPath}
                  />
                ),
              },
              {
                key: 'source',
                label: 'YAML 源码',
                children: (
                  <Suspense fallback={<Spin />}>
                    <SourceView
                      text={draft.text}
                      onChange={draft.setText}
                      schema={schema.data}
                      issues={issues}
                      revealLine={reveal}
                      height="calc(100vh - 240px)"
                    />
                  </Suspense>
                ),
              },
              {
                key: 'effective',
                label: '有效值',
                children: (
                  <EffectiveView
                    model={draft.model}
                    savedHash={draft.base.hash}
                    dirty={draft.dirty}
                    host={scope.kind === 'host' ? scope.host : search.host}
                    onHostChange={(h) => setSearch({ host: h })}
                  />
                ),
              },
              {
                key: 'matrix',
                label: '节点矩阵',
                children: (
                  <MatrixView
                    model={draft.model}
                    onSelectHost={(group, host) => setSearch({ scope: scopeKey({ kind: 'host', group, host }), view: 'form' })}
                  />
                ),
              },
            ]}
          />
        </Card>

        <Card
          size="small"
          title={
            <Segmented
              size="small"
              value={side}
              onChange={(v) => setSide(v as 'issues' | 'memory')}
              options={[
                { value: 'issues', label: `校验${errorCount ? `（${errorCount}）` : ''}` },
                { value: 'memory', label: '内存预估' },
              ]}
            />
          }
          styles={{ body: { height: `calc(${PANEL_HEIGHT} - 40px)`, overflow: 'auto' } }}
        >
          {side === 'issues' ? (
            <IssuesPanel
              issues={issues}
              serverChecking={serverCheck.isPending}
              serverCheckedAt={serverIssues?.text === draft.text ? 1 : undefined}
              onServerCheck={() => serverCheck.mutate()}
              onJumpLine={jumpLine}
            />
          ) : (
            <MemoryPanel estimates={estimates} saved={savedEstimates} scope={scope} model={draft.model} />
          )}
        </Card>
      </div>

      <SaveDialog
        open={saveOpen}
        onClose={() => setSaveOpen(false)}
        baseText={draft.base.text}
        baseHash={draft.draft?.baseHash ?? draft.base.hash}
        text={draft.text}
        localIssues={localIssues}
        onRebase={draft.rebase}
        onSaved={() => {
          setSaveOpen(false)
          draft.discard()
        }}
      />
    </div>
  )
}
