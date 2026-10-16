// 本地 worker 入口：直接对依赖用 ?worker 会被 Vite 依赖预构建处理，导致没有 default 导出
import 'monaco-editor/editor/editor.worker'
