import { DiffEditor } from '@monaco-editor/react'
import '@/lib/monaco'

interface Props {
  original: string
  modified: string
  height: string | number
  originalTitle?: string
  modifiedTitle?: string
}

export default function DiffView({ original, modified, height, originalTitle, modifiedTitle }: Props) {
  return (
    <div>
      {(originalTitle || modifiedTitle) && (
        <div style={{ display: 'flex', fontSize: 12, color: '#888', marginBottom: 4 }}>
          <div style={{ flex: 1 }}>{originalTitle}</div>
          <div style={{ flex: 1 }}>{modifiedTitle}</div>
        </div>
      )}
      <DiffEditor
        height={height}
        language="yaml"
        original={original}
        modified={modified}
        options={{
          readOnly: true,
          renderSideBySide: true,
          minimap: { enabled: false },
          fontSize: 12,
          scrollBeyondLastLine: false,
          hideUnchangedRegions: { enabled: true },
          automaticLayout: true,
        }}
      />
    </div>
  )
}
