import { fileURLToPath, URL } from 'node:url'
import { tanstackRouter } from '@tanstack/router-plugin/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

const apiTarget = process.env.CONSOLE_API ?? 'http://127.0.0.1:8860'

export default defineConfig({
  plugins: [
    tanstackRouter({ target: 'react', autoCodeSplitting: true }),
    react(),
  ],
  resolve: {
    alias: [
      { find: '@', replacement: fileURLToPath(new URL('./src', import.meta.url)) },
      // monaco-editor 0.5x 的 exports 映射把 esm/vs 前缀又拼了一次，monaco-yaml 依赖的
      // monaco-worker-manager 仍按旧路径导入 worker，这里直接指向真实文件
      {
        find: /^monaco-editor\/esm\/vs\/([^?]*?)(\.js)?(\?.*)?$/,
        replacement: fileURLToPath(new URL('./node_modules/monaco-editor/esm/vs/', import.meta.url)) + '$1.js$3',
      },
    ],
  },
  server: {
    port: 5173,
    proxy: { '/api': { target: apiTarget, changeOrigin: false } },
  },
  build: {
    chunkSizeWarningLimit: 4096,
  },
})
