/**
 * 以「文本补丁」方式修改 YAML：只替换被修改节点对应的源码区间，其余字节保持不变。
 *
 * eemeli/yaml 的 Document.toString() 会重排 Pigsty 惯用的 `{ a: 1 ,b: 2 }` 写法，
 * 在 conf/ 下 53 个文件上没有一个能字节级往返，所以不能整篇重新序列化。
 */
import { isMap, isScalar, isSeq, parseDocument, Scalar, stringify } from 'yaml'
import type { Document, Node, Pair, YAMLMap } from 'yaml'

export type Path = (string | number)[]

export function parse(text: string): Document.Parsed {
  return parseDocument(text, { keepSourceTokens: true, uniqueKeys: false })
}

function lineStart(text: string, pos: number) {
  return text.lastIndexOf('\n', pos - 1) + 1
}

function column(text: string, pos: number) {
  return pos - lineStart(text, pos)
}

// 节点结束位置推到所在行尾（含换行），保证插入/删除以整行为单位
function lineEndAfter(text: string, pos: number) {
  // 块集合的结束位置可能落在下一个键的缩进里，此时退回到该行行首
  const ls = lineStart(text, pos)
  if (/^[ \t]*$/.test(text.slice(ls, pos))) return ls
  const nl = text.indexOf('\n', pos)
  return nl === -1 ? text.length : nl + 1
}

function splice(text: string, start: number, end: number, insert: string) {
  return text.slice(0, start) + insert + text.slice(end)
}

const PLAIN_KEY = /^[A-Za-z0-9_.\-/$]+$/

function formatKey(key: string | number) {
  const s = String(key)
  return PLAIN_KEY.test(s) ? s : JSON.stringify(s)
}

/** 单行内联写法：标量、flow 序列、flow 映射 */
export function formatInline(value: unknown): string {
  if (value === null || value === undefined) return 'null'
  if (Array.isArray(value)) return value.length ? `[ ${value.map(formatInline).join(', ')} ]` : '[]'
  if (typeof value === 'object') {
    const entries = Object.entries(value as Record<string, unknown>)
    return entries.length ? `{ ${entries.map(([k, v]) => `${formatKey(k)}: ${formatInline(v)}`).join(', ')} }` : '{}'
  }
  return stringify(value, { lineWidth: 0 }).trimEnd()
}

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

/**
 * 块写法，按 Pigsty 习惯：
 * - 映射 → 块映射
 * - 元素都是对象的数组 → 块序列，每项一行 flow 映射
 * - 其他数组、标量 → 内联
 * 返回值以 `key:` 之后的部分开头（内联时为 " xxx"，块写法时为 "\n..."，不含结尾换行）
 */
function formatValueAfterKey(value: unknown, childIndent: number): string {
  const pad = ' '.repeat(childIndent)
  if (isPlainObject(value) && Object.keys(value).length > 0) {
    return '\n' + Object.entries(value).map(([k, v]) => `${pad}${formatKey(k)}:${formatValueAfterKey(v, childIndent + 2)}`).join('\n')
  }
  if (Array.isArray(value) && value.length > 0 && value.every(isPlainObject)) {
    return '\n' + value.map((v) => `${pad}- ${formatInline(v)}`).join('\n')
  }
  return ' ' + formatInline(value)
}

function nodeAt(doc: Document.Parsed, path: Path): Node | undefined {
  if (path.length === 0) return doc.contents ?? undefined
  return doc.getIn(path, true) as Node | undefined
}

function pairOf(doc: Document.Parsed, path: Path): Pair<Scalar, Node | null> | undefined {
  const parent = nodeAt(doc, path.slice(0, -1))
  if (!isMap(parent)) return undefined
  const key = path[path.length - 1]
  return (parent as YAMLMap<Scalar, Node | null>).items.find((p) => isScalar(p.key) && String(p.key.value) === String(key))
}

/** 替换 pair 的值节点（flow 节点保持 flow；块节点按原缩进重写） */
function replaceNode(text: string, pair: Pair<Scalar, Node | null>, value: unknown, keyIndent: number): string {
  const node = pair.value!
  const [start, valueEnd] = node.range!
  const asBlock = (isPlainObject(value) || Array.isArray(value)) && formatValueAfterKey(value, 0).startsWith('\n')
  if (isScalar(node) && node.value === null && asBlock) {
    // `key: null` 或 `key: ~` 改成块集合
    return splice(text, text.indexOf(':', pair.key.range![1]) + 1, valueEnd, formatValueAfterKey(value, keyIndent + 2))
  }
  if (isScalar(node) || (node as { flow?: boolean }).flow || !asBlock) {
    if (!isScalar(node) && !(node as { flow?: boolean }).flow) {
      // 块集合改成内联值：从冒号后重写到块结束
      const colon = text.indexOf(':', pair.key.range![1])
      return splice(text, colon + 1, lineEndAfter(text, node.range![2]), ' ' + formatInline(value) + '\n')
    }
    // 标量保持原引号风格
    if (isScalar(node) && typeof value === 'string' && (node.type === 'QUOTE_DOUBLE' || node.type === 'QUOTE_SINGLE')) {
      const s = new Scalar(value)
      s.type = node.type
      return splice(text, start, valueEnd, stringify(s, { lineWidth: 0 }).trimEnd())
    }
    return splice(text, start, valueEnd, formatInline(value))
  }
  // 块集合换块集合：保留 `key:  # 注释` 这一行，只重写下面的块
  const keyLineEnd = text.indexOf('\n', pair.key.range![1])
  const end = lineEndAfter(text, node.range![2])
  return splice(text, keyLineEnd, end, formatValueAfterKey(value, keyIndent + 2) + '\n')
}

/** 设置 map 中某个键的值；键不存在时新增。path 的最后一段是键名。 */
export function setIn(text: string, path: Path, value: unknown): string {
  const doc = parse(text)
  const pair = pairOf(doc, path)
  if (pair) {
    const keyStart = pair.key.range![0]
    const keyIndent = column(text, keyStart)
    if (pair.value && pair.value.range) {
      // 原值为空（`key:` 或 `key: null`）且新值是块集合时，按块写法重写
      return replaceNode(text, pair, value, keyIndent)
    }
    const keyEnd = pair.key.range![1]
    const colon = text.indexOf(':', keyEnd)
    return splice(text, colon + 1, lineEndAfter(text, colon), formatValueAfterKey(value, keyIndent + 2) + '\n')
  }

  const mapPath = path.slice(0, -1)
  const key = path[path.length - 1]
  const map = nodeAt(doc, mapPath)
  if (map === undefined || (isScalar(map) && map.value === null)) {
    // 父级不存在或为空，递归在祖先上创建
    if (mapPath.length === 0) throw new Error('文档根节点不是映射')
    return setIn(text, mapPath, { [key]: value })
  }
  if (!isMap(map)) throw new Error(`${mapPath.join('.')} 不是映射`)

  if (map.flow || map.items.length === 0) {
    const obj = { ...(map.toJSON() as Record<string, unknown>), [key]: value }
    return splice(text, map.range![0], map.range![1], formatInline(obj))
  }
  const first = map.items[0] as Pair<Scalar, Node | null>
  const indent = column(text, first.key.range![0])
  const last = map.items[map.items.length - 1] as Pair<Scalar, Node | null>
  const lastEnd = last.value?.range ? last.value.range[2] : last.key.range![2]
  const at = lineEndAfter(text, lastEnd)
  const prefix = at === text.length && !text.endsWith('\n') ? '\n' : ''
  const line = `${' '.repeat(indent)}${formatKey(key)}:${formatValueAfterKey(value, indent + 2)}\n`
  return splice(text, at, at, prefix + line)
}

/** 删除 map 中的键。块映射删除整行（含该键的块值）；flow 映射局部重写。 */
export function deleteIn(text: string, path: Path): string {
  const doc = parse(text)
  const pair = pairOf(doc, path)
  if (!pair) return text
  const map = nodeAt(doc, path.slice(0, -1)) as YAMLMap
  if (map.flow) {
    const obj = { ...(map.toJSON() as Record<string, unknown>) }
    delete obj[String(path[path.length - 1])]
    return splice(text, map.range![0], map.range![1], formatInline(obj))
  }
  if (map.items.length === 1 && path.length > 1) {
    // 块映射删空后整个父键一起删除，避免留下 `vars:` 这样的空值
    return deleteIn(text, path.slice(0, -1))
  }
  const start = lineStart(text, pair.key.range![0])
  const end = lineEndAfter(text, pair.value?.range ? pair.value.range[2] : pair.key.range![2])
  return splice(text, start, end, '')
}

/** 块映射写法（用于块序列中的多行对象项），首行不缩进，后续行缩进 indent */
function formatBlockMap(value: Record<string, unknown>, indent: number): string {
  const pad = ' '.repeat(indent)
  return Object.entries(value)
    .map(([k, v], i) => `${i ? pad : ''}${formatKey(k)}:${formatValueAfterKey(v, indent + 2)}`)
    .join('\n')
}

function seqAt(doc: Document.Parsed, path: Path) {
  const seq = nodeAt(doc, path)
  if (!isSeq(seq)) throw new Error(`${path.join('.')} 不是列表`)
  return seq
}

/** 替换列表中的第 index 项；flow 项保持单行，块映射项按原缩进重写，其余项不动 */
export function setItem(text: string, path: Path, index: number, value: unknown): string {
  const doc = parse(text)
  const seq = seqAt(doc, path)
  const item = seq.items[index] as Node | undefined
  if (!item?.range) throw new Error(`列表项 ${index} 不存在`)
  if (seq.flow) {
    const arr = seq.toJSON() as unknown[]
    arr[index] = value
    return splice(text, seq.range![0], seq.range![1], formatInline(arr))
  }
  if (isMap(item) && !item.flow && isPlainObject(value) && Object.keys(value).length > 1) {
    const end = lineEndAfter(text, item.range[2])
    return splice(text, item.range[0], end, formatBlockMap(value, column(text, item.range[0])) + '\n')
  }
  return splice(text, item.range[0], item.range[1], formatInline(value))
}

/** 在列表末尾追加一项；列表不存在时创建 */
export function appendItem(text: string, path: Path, value: unknown): string {
  const doc = parse(text)
  const seq = nodeAt(doc, path)
  if (seq === undefined || (isScalar(seq) && seq.value === null)) return setIn(text, path, [value])
  if (!isSeq(seq)) throw new Error(`${path.join('.')} 不是列表`)
  if (seq.flow || seq.items.length === 0) {
    return splice(text, seq.range![0], seq.range![1], formatInline([...(seq.toJSON() as unknown[]), value]))
  }
  const first = seq.items[0] as Node
  const dash = text.lastIndexOf('-', first.range![0])
  const indent = column(text, dash)
  const last = seq.items[seq.items.length - 1] as Node
  const at = lineEndAfter(text, last.range![2])
  const prefix = at === text.length && !text.endsWith('\n') ? '\n' : ''
  return splice(text, at, at, `${prefix}${' '.repeat(indent)}- ${formatInline(value)}\n`)
}

/** 删除列表中的第 index 项 */
export function deleteItem(text: string, path: Path, index: number): string {
  const doc = parse(text)
  const seq = seqAt(doc, path)
  const item = seq.items[index] as Node | undefined
  if (!item?.range) return text
  if (seq.flow || seq.items.length === 1) {
    const arr = seq.toJSON() as unknown[]
    arr.splice(index, 1)
    if (!seq.flow) return setIn(text, path, [])
    return splice(text, seq.range![0], seq.range![1], formatInline(arr))
  }
  const start = lineStart(text, item.range[0])
  return splice(text, start, lineEndAfter(text, item.range[2]), '')
}

/** 读取节点的 JS 值 */
export function getIn(doc: Document.Parsed, path: Path): unknown {
  const n = nodeAt(doc, path)
  if (n === undefined) return undefined
  return isScalar(n) ? n.value : isMap(n) || isSeq(n) ? n.toJSON() : n
}

/** 节点所在行号（1-based），用于从表单跳到源码 */
export function lineOf(text: string, doc: Document.Parsed, path: Path): number | undefined {
  const pair = path.length ? pairOf(doc, path) : undefined
  const pos = pair?.key.range?.[0] ?? nodeAt(doc, path)?.range?.[0]
  if (pos === undefined) return undefined
  return text.slice(0, pos).split('\n').length
}
