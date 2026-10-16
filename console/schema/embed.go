// Package schemafiles carries the generated parameter schemas so that the
// console binary stays self-contained.
package schemafiles

import "embed"

// Files holds every generated schema file, e.g. v4.5.0.json.
//
//go:embed *.json
var Files embed.FS
