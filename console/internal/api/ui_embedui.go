//go:build embedui

package api

import (
	"io/fs"

	"github.com/dbstd2333/pigsty-ui/console/web"
)

// uiFS serves the frontend build embedded by console/web/embed.go.
var uiFS fs.FS = mustSub()

// mustSub exposes the dist directory of the embedded frontend build.
func mustSub() fs.FS {
	sub, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		panic("embedded frontend build is missing: " + err.Error())
	}
	return sub
}
