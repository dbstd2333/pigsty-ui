import { useEffect, useRef } from 'react'
import Editor from '@monaco-editor/react'
import type { Issue, ParamSchema } from '@/api/types'
import { monaco, PIGSTY_MODEL_URI, setYamlSchema } from '@/lib/monaco'
import { toJsonSchema } from './jsonSchema'

interface Props {
  text: string
  onChange: (text: string) => void
  schema?: ParamSchema
  issues: Issue[]
  revealLine?: { line: number; nonce: number }
  height: string | number
}

export default function SourceView({ text, onChange, schema, issues, revealLine, height }: Props) {
  const editorRef = useRef<monaco.editor.IStandaloneCodeEditor | null>(null)

  useEffect(() => {
    if (schema) setYamlSchema(toJsonSchema(schema))
  }, [schema])

  // 把前端规则与服务端校验结果标在编辑器里
  useEffect(() => {
    const model = monaco.editor.getModel(monaco.Uri.parse(PIGSTY_MODEL_URI))
    if (!model) return
    const markers = issues
      .filter((i) => i.line && i.source !== 'yaml')
      .map((i) => ({
        severity: i.level === 'error' ? monaco.MarkerSeverity.Error : monaco.MarkerSeverity.Warning,
        message: i.message,
        startLineNumber: i.line!,
        endLineNumber: i.line!,
        startColumn: 1,
        endColumn: model.getLineMaxColumn(Math.min(i.line!, model.getLineCount())),
      }))
    monaco.editor.setModelMarkers(model, 'pigsty', markers)
  }, [issues, text])

  useEffect(() => {
    const ed = editorRef.current
    if (!ed || !revealLine) return
    ed.revealLineInCenter(revealLine.line)
    ed.setPosition({ lineNumber: revealLine.line, column: 1 })
    ed.focus()
  }, [revealLine])

  return (
    <Editor
      height={height}
      language="yaml"
      path={PIGSTY_MODEL_URI}
      value={text}
      onChange={(v) => v !== undefined && v !== text && onChange(v)}
      onMount={(ed) => {
        editorRef.current = ed
        if (revealLine) {
          ed.revealLineInCenter(revealLine.line)
          ed.setPosition({ lineNumber: revealLine.line, column: 1 })
        }
      }}
      options={{
        minimap: { enabled: false },
        fontSize: 13,
        tabSize: 2,
        insertSpaces: true,
        scrollBeyondLastLine: false,
        renderWhitespace: 'boundary',
        wordWrap: 'off',
        automaticLayout: true,
      }}
    />
  )
}
