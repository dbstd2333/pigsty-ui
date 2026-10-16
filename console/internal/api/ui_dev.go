//go:build !embedui

package api

import "io/fs"

// uiFS is empty unless the frontend is embedded with the `embedui` build tag.
var uiFS fs.FS
