package api

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// uiHandler serves the embedded single page application, or a hint page when
// the binary was built without the frontend.
func (s *Server) uiHandler() http.Handler {
	if uiFS == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				serveAPINotFound(w)
				return
			}
			serveDevNotice(w)
		})
	}
	files := http.FileServer(http.FS(uiFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			serveAPINotFound(w)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name != "." && name != "" {
			if st, err := fs.Stat(uiFS, name); err == nil && !st.IsDir() {
				files.ServeHTTP(w, r)
				return
			}
		}
		// Unknown paths fall back to index.html so client side routing works.
		serveIndex(w)
	})
}

// serveAPINotFound answers unknown API routes with a problem+json body, so
// clients never receive an HTML page from the API namespace.
func serveAPINotFound(w http.ResponseWriter) {
	body := &ProblemError{Title: titleFor(http.StatusNotFound), Status: http.StatusNotFound, Detail: "接口不存在"}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusNotFound)
	if data, err := json.Marshal(body); err == nil {
		w.Write(data)
	}
}

// serveIndex writes the embedded index.html.
func serveIndex(w http.ResponseWriter) {
	data, err := fs.ReadFile(uiFS, "index.html")
	if err != nil {
		http.Error(w, "frontend build is missing index.html", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

// devNotice explains how to reach the frontend development server.
func serveDevNotice(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(devNoticeHTML))
}

// devNoticeHTML is shown when the frontend was not embedded into the binary.
const devNoticeHTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<title>Pigsty Console</title>
<style>
body { font-family: -apple-system, "PingFang SC", "Microsoft YaHei", sans-serif; margin: 4rem auto; max-width: 42rem; line-height: 1.7; color: #24292f; }
code { background: #f6f8fa; padding: .15rem .35rem; border-radius: 4px; }
</style>
</head>
<body>
<h1>Pigsty Console API</h1>
<p>当前二进制未内嵌前端页面。开发时请访问 Vite 开发服务器：</p>
<pre><code>pnpm -C web dev   # 默认 http://localhost:5173</code></pre>
<p>Vite 会把 <code>/api</code> 代理到本服务（默认 <code>127.0.0.1:8860</code>）。</p>
<p>发布构建请使用 <code>make build</code>（<code>pnpm -C web build &amp;&amp; go build -tags embedui</code>）。</p>
<p>API 入口：<code>/api/v1</code>，OpenAPI 文档：<code>/api/v1/openapi.json</code>。</p>
</body>
</html>
`
