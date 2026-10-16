import { useCallback, useEffect, useMemo, useState } from 'react'
import { useConfig } from '@/api/queries'
import { buildModel } from './model'
import { parse } from './yamlPatch'

const STORAGE_KEY = 'pigsty-console:draft'

interface StoredDraft {
  baseHash: string
  text: string
}

function loadStored(): StoredDraft | null {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    return raw ? (JSON.parse(raw) as StoredDraft) : null
  } catch {
    return null
  }
}

function store(d: StoredDraft | null) {
  try {
    if (d) localStorage.setItem(STORAGE_KEY, JSON.stringify(d))
    else localStorage.removeItem(STORAGE_KEY)
  } catch {
    // 浏览器存储不可用时草稿只保存在内存里
  }
}

/**
 * 在工作台之外（向导、集群页）修改草稿：在现有草稿（没有则用服务端版本）上应用 fn，
 * 然后跳转到工作台由用户核对、保存。草稿基于旧版本时保留其 baseHash，冲突由保存流程处理。
 */
export function editDraft(base: { text: string; hash: string }, fn: (text: string) => string) {
  const cur = loadStored()
  const d = { baseHash: cur?.baseHash ?? base.hash, text: fn(cur?.text ?? base.text) }
  store(d)
  window.dispatchEvent(new Event(DRAFT_EVENT))
}

export function hasDraft() {
  return loadStored() !== null
}

const DRAFT_EVENT = 'pigsty-console:draft-changed'

/**
 * 工作台草稿：
 * - base：服务端已保存的版本（text + hash）
 * - draft：本地未保存的修改，带上它基于的 baseHash，刷新页面不丢
 * - 服务端版本在编辑期间被他人或手工改动时 stale = true
 */
export function useConfigDraft() {
  const server = useConfig()
  const [draft, setDraftState] = useState<StoredDraft | null>(() => loadStored())

  const base = server.data
  const text = draft?.text ?? base?.text ?? ''
  const dirty = !!draft && !!base && draft.text !== base.text
  const stale = !!draft && !!base && draft.baseHash !== base.hash

  const setText = useCallback(
    (next: string) => {
      if (!base) return
      setDraftState((prev) => {
        const d = { baseHash: prev?.baseHash ?? base.hash, text: next }
        store(d)
        return d
      })
    },
    [base],
  )

  const discard = useCallback(() => {
    store(null)
    setDraftState(null)
  }, [])

  // 冲突处理：确认后把草稿改为基于最新服务端版本
  const rebase = useCallback((hash: string) => {
    setDraftState((prev) => {
      if (!prev) return prev
      const d = { ...prev, baseHash: hash }
      store(d)
      return d
    })
  }, [])

  // 其他页面通过 editDraft 修改后同步
  useEffect(() => {
    const sync = () => setDraftState(loadStored())
    window.addEventListener(DRAFT_EVENT, sync)
    return () => window.removeEventListener(DRAFT_EVENT, sync)
  }, [])

  // 草稿与服务端完全一致时自动清掉
  useEffect(() => {
    if (draft && base && draft.text === base.text) discard()
  }, [draft, base, discard])

  const doc = useMemo(() => parse(text), [text])
  const model = useMemo(() => buildModel(doc.errors.length ? null : doc.toJS()), [doc])

  return { server, base, text, draft, dirty, stale, setText, discard, rebase, doc, model }
}

export type ConfigDraft = ReturnType<typeof useConfigDraft>
