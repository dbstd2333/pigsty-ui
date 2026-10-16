//go:build embedui

// Package web embeds the built frontend when the console is compiled with the
// `embedui` build tag: go build -tags embedui.
package web

import "embed"

// Dist holds the Vite build output of console/web.
//
//go:embed all:dist
var Dist embed.FS
