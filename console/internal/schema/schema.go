// Package schema loads the generated Pigsty parameter schema and answers
// questions about individual parameters.
package schema

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
)

// Param describes one Pigsty parameter.
type Param struct {
	Module    string          `json:"module"`
	Section   string          `json:"section,omitempty"`
	Role      string          `json:"role,omitempty"`
	Level     string          `json:"level"`
	Type      string          `json:"type"`
	Default   json.RawMessage `json:"default,omitempty"`
	Enum      []any           `json:"enum,omitempty"`
	Identity  bool            `json:"identity,omitempty"`
	Sensitive bool            `json:"sensitive,omitempty"`
	Impact    string          `json:"impact,omitempty"`
	Desc      string          `json:"desc,omitempty"`
	Label     string          `json:"label,omitempty"`
	Doc       string          `json:"doc,omitempty"`
	Items     any             `json:"items,omitempty"`
	Ref       string          `json:"$ref,omitempty"`
}

// File is the full parameter schema document served by GET /api/v1/schema.
type File struct {
	Version string           `json:"version"`
	Modules []string         `json:"modules"`
	Params  map[string]Param `json:"params"`
	Defs    map[string]any   `json:"defs"`
}

// sensitiveKeyPattern matches parameter names that must always be masked.
var sensitiveKeyPattern = regexp.MustCompile(`(?i)password|secret|_key|pass`)

// selectName picks the schema file matching a Pigsty version, falling back to
// the newest embedded file when that version is unknown.
func selectName(fsys fs.FS, version string) (string, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return "", fmt.Errorf("read schema dir: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("no schema file available")
	}
	sort.Strings(names)
	want := "v" + strings.TrimPrefix(strings.TrimSpace(version), "v") + ".json"
	for _, n := range names {
		if n == want {
			return n, nil
		}
	}
	return names[len(names)-1], nil
}

// Read returns the raw schema JSON of the requested Pigsty version.
func Read(fsys fs.FS, version string) ([]byte, error) {
	name, err := selectName(fsys, version)
	if err != nil {
		return nil, err
	}
	return fs.ReadFile(fsys, name)
}

// Load reads and parses the schema of the requested Pigsty version.
func Load(fsys fs.FS, version string) (*Set, error) {
	data, err := Read(fsys, version)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse parses a schema document.
func Parse(data []byte) (*Set, error) {
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse schema: %w", err)
	}
	return &Set{File: &f}, nil
}

// Set is a parsed schema document with lookup helpers.
type Set struct {
	File *File
}

// Sensitive reports whether a parameter value must be masked. It follows the
// schema flag first and falls back to a name pattern.
func (s *Set) Sensitive(key string) bool {
	if p, ok := s.File.Params[key]; ok && p.Sensitive {
		return true
	}
	return SensitiveKey(key)
}

// SensitiveKey reports whether a parameter name looks sensitive.
func SensitiveKey(key string) bool { return sensitiveKeyPattern.MatchString(key) }

// Default returns the schema default of a parameter, if any.
func (s *Set) Default(key string) (any, bool) {
	p, ok := s.File.Params[key]
	if !ok || len(p.Default) == 0 {
		return nil, false
	}
	var v any
	if err := json.Unmarshal(p.Default, &v); err != nil {
		return nil, false
	}
	return v, true
}

// Defaults returns every parameter that has a schema default.
func (s *Set) Defaults() map[string]any {
	out := make(map[string]any, len(s.File.Params))
	for key, p := range s.File.Params {
		if len(p.Default) == 0 {
			continue
		}
		var v any
		if err := json.Unmarshal(p.Default, &v); err != nil {
			continue
		}
		out[key] = v
	}
	return out
}
