// Package main implements tools/schemagen, which derives the Pigsty parameter
// schema from roles/*/defaults/main.yml plus schema/overrides.yaml.
package main

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// param is one parameter definition found in a role defaults file.
type param struct {
	Key        string
	Role       string
	Section    string
	Line       int
	Level      string
	Desc       string
	Type       string
	Default    any
	HasDefault bool
	// Referenced marks definitions copied into a role as a reference block
	// (section title suffixed with "(Reference)").
	Referenced bool
}

// dividerPattern matches the "#-----" comment rules used between sections.
var dividerPattern = regexp.MustCompile(`^#-{5,}\s*$`)

// keyPattern matches top level parameter keys; Pigsty params are lower snake case.
var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// levelTokens are the layer markers that may appear right after a value.
var levelTokens = map[string]bool{
	"GLOBAL":   true,
	"CLUSTER":  true,
	"INSTANCE": true,
	"NODE":     true,
	"APP":      true,
	"DOCKER":   true,
}

// markerTokens are bracket markers that carry no layer information.
var markerTokens = map[string]bool{"REQUIRED": true, "OPTIONAL": true}

// parseRoleDefaults extracts every parameter from one role defaults file.
func parseRoleDefaults(role string, data []byte) []param {
	lines := strings.Split(string(data), "\n")
	var out []param
	section, referenced := "", false
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		// Section titles sit between two divider rules: the opening rule is
		// followed by the title, the closing rule is preceded by it.
		if dividerPattern.MatchString(trimmed) {
			if title, ok := sectionTitle(lines, i); ok {
				referenced = strings.Contains(strings.ToLower(title), "(reference)")
				section = cleanSection(title)
			}
			continue
		}
		if trimmed == "" || trimmed == "---" {
			continue
		}

		key, rest, commented, ok := parseKeyLine(line)
		if !ok {
			continue
		}
		valueText, comment := splitValueComment(rest)
		level, desc := parseLevelDesc(comment)
		typ, def, hasDef := inferValue(valueText, collectBlock(lines, i), commented)
		out = append(out, param{
			Key:        key,
			Role:       role,
			Section:    section,
			Line:       i + 1,
			Level:      level,
			Desc:       desc,
			Type:       typ,
			Default:    def,
			HasDefault: hasDef,
			Referenced: referenced,
		})
	}
	return out
}

// sectionTitle returns the title that follows an opening divider rule. A rule
// that closes a header block is preceded by its title and yields no title.
func sectionTitle(lines []string, i int) (string, bool) {
	if prev := previousContentLine(lines, i); prev != "" && isTitleComment(prev) {
		return "", false
	}
	for j := i + 1; j < len(lines); j++ {
		next := strings.TrimSpace(lines[j])
		if next == "" {
			continue
		}
		if dividerPattern.MatchString(next) {
			return "", false
		}
		if isTitleComment(next) {
			return strings.TrimSpace(strings.TrimPrefix(next, "#")), true
		}
		return "", false
	}
	return "", false
}

// previousContentLine returns the closest non-empty line before index i.
func previousContentLine(lines []string, i int) string {
	for j := i - 1; j >= 0; j-- {
		if s := strings.TrimSpace(lines[j]); s != "" {
			return s
		}
	}
	return ""
}

// isTitleComment reports whether a comment can be a section title. Titles are
// plain upper case headings, never commented-out parameters.
func isTitleComment(line string) bool {
	if !strings.HasPrefix(line, "#") || dividerPattern.MatchString(line) {
		return false
	}
	if _, _, _, ok := parseKeyLine(line); ok {
		return false
	}
	fields := strings.Fields(strings.TrimLeft(line, "#"))
	if len(fields) == 0 {
		return false
	}
	return fields[0] == strings.ToUpper(fields[0])
}

// parseKeyLine splits a top level mapping line, accepting commented-out keys.
func parseKeyLine(line string) (key, rest string, commented, ok bool) {
	// Indented lines belong to the previous key, never to a new parameter.
	if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
		return "", "", false, false
	}
	s := line
	if strings.HasPrefix(s, "#") {
		commented = true
		s = strings.TrimSpace(strings.TrimLeft(s, "#"))
	}
	idx := strings.Index(s, ":")
	if idx < 0 {
		return "", "", false, false
	}
	key = strings.TrimSpace(s[:idx])
	if !keyPattern.MatchString(key) {
		return "", "", false, false
	}
	return key, strings.TrimSpace(s[idx+1:]), commented, true
}

// splitValueComment splits "value  # comment" honoring quotes.
func splitValueComment(s string) (value, comment string) {
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == '\\' && quote == '"' && i+1 < len(s) {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '#':
			if i == 0 || s[i-1] == ' ' || s[i-1] == '\t' {
				return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i:])
			}
		}
	}
	return strings.TrimSpace(s), ""
}

// parseLevelDesc extracts an optional layer marker and the description text.
func parseLevelDesc(comment string) (level, desc string) {
	comment = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(comment), "#"))
	if comment == "" {
		return "", ""
	}
	var parts []string
	for _, seg := range strings.Split(comment, "#") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		if token, ok := bracketToken(seg); ok {
			if levelTokens[token] && level == "" {
				level = token
				continue
			}
			if markerTokens[token] {
				continue
			}
		}
		parts = append(parts, seg)
	}
	return level, strings.Join(parts, " # ")
}

// bracketToken returns the upper-cased token of "[x]" or "X" segments.
func bracketToken(seg string) (string, bool) {
	if strings.HasPrefix(seg, "[") && strings.HasSuffix(seg, "]") {
		inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(seg, "["), "]"))
		if inner == "" || strings.ContainsAny(inner, ",:") {
			return "", false
		}
		return strings.ToUpper(inner), true
	}
	if seg == strings.ToUpper(seg) && !strings.ContainsAny(seg, " ,.:;") {
		return seg, true
	}
	return "", false
}

// collectBlock returns the indented continuation lines that belong to a key.
func collectBlock(lines []string, i int) []string {
	var block []string
	for j := i + 1; j < len(lines); j++ {
		line := lines[j]
		if strings.TrimSpace(line) == "" {
			break
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			break
		}
		block = append(block, line)
	}
	return block
}

// inferValue derives the parameter type and default value.
func inferValue(raw string, block []string, commented bool) (typ string, def any, has bool) {
	text := strings.TrimSpace(raw)
	if text != "" {
		if commented {
			// Commented-out keys document an example, never a default.
			return typeOfYAML(text), nil, false
		}
		var v any
		if err := yaml.Unmarshal([]byte(text), &v); err == nil {
			return typeOf(v), v, true
		}
		return "string", text, true
	}
	if len(block) > 0 {
		var m map[string]any
		if err := yaml.Unmarshal([]byte("x:\n"+strings.Join(block, "\n")), &m); err == nil {
			if v, ok := m["x"]; ok && v != nil {
				return typeOf(v), v, true
			}
		}
		if strings.HasPrefix(strings.TrimSpace(block[0]), "- ") {
			return "array", nil, false
		}
		return "object", nil, false
	}
	return "any", nil, false
}

// typeOfYAML infers a type from a literal sample value without keeping it.
func typeOfYAML(text string) string {
	var v any
	if err := yaml.Unmarshal([]byte(text), &v); err != nil {
		return "string"
	}
	return typeOf(v)
}

// typeOf maps a decoded YAML value onto a schema type name.
func typeOf(v any) string {
	switch v.(type) {
	case nil:
		return "any"
	case bool:
		return "boolean"
	case int, int64, uint64:
		return "integer"
	case float32, float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "string"
	}
}

// cleanSection normalizes a section title, dropping "(Reference)" suffixes.
func cleanSection(title string) string {
	title = strings.TrimSpace(title)
	if idx := strings.Index(title, "(Reference)"); idx > 0 {
		title = strings.TrimSpace(title[:idx])
	}
	if idx := strings.Index(title, "  "); idx > 0 {
		title = strings.TrimSpace(title[:idx])
	}
	return title
}

// roleModules maps a role directory onto the module shown in the console.
var roleModules = map[string]string{
	"pgsql":        "PGSQL",
	"pg_id":        "PGSQL",
	"pg_remove":    "PGSQL",
	"pg_monitor":   "PGSQL",
	"pg_exporters": "PGSQL",
	"pg_pitr":      "PGSQL",
	"pg_migration": "PGSQL",
	"node":         "NODE",
	"node_id":      "NODE",
	"node_monitor": "NODE",
	"node_remove":  "NODE",
	"haproxy":      "NODE",
	"infra":        "INFRA",
	"ca":           "INFRA",
	"repo":         "INFRA",
	"cache":        "INFRA",
	"etcd":         "ETCD",
	"etcd_remove":  "ETCD",
	"minio":        "MINIO",
	"minio_remove": "MINIO",
	"redis":        "REDIS",
	"redis_remove": "REDIS",
	"docker":       "DOCKER",
	"mysql":        "MYSQL",
	"mysql_remove": "MYSQL",
	"kafka":        "KAFKA",
	"kafka_remove": "KAFKA",
	"juice":        "OTHER",
	"vibe":         "OTHER",
}

// primaryRole is the role that owns a module's main parameter set.
var primaryRole = map[string]string{
	"INFRA":  "infra",
	"NODE":   "node",
	"PGSQL":  "pgsql",
	"ETCD":   "etcd",
	"MINIO":  "minio",
	"REDIS":  "redis",
	"DOCKER": "docker",
	"MYSQL":  "mysql",
	"KAFKA":  "kafka",
}

// moduleOf returns the module for a role, defaulting to OTHER.
func moduleOf(role string) string {
	if m, ok := roleModules[role]; ok {
		return m
	}
	return "OTHER"
}

// ownerRank scores a definition; lower wins when several roles define a key.
func ownerRank(p param) int {
	switch {
	case primaryRole[moduleOf(p.Role)] == p.Role && !p.Referenced:
		return 0
	case !strings.HasSuffix(p.Role, "_remove") && !p.Referenced:
		return 1
	case !strings.HasSuffix(p.Role, "_remove"):
		return 2
	default:
		return 3
	}
}

// moduleOrder is the canonical ordering of modules in the generated schema.
var moduleOrder = []string{"INFRA", "NODE", "ETCD", "MINIO", "PGSQL", "REDIS", "DOCKER", "APP", "MYSQL", "KAFKA", "OTHER"}

// orderModules sorts the modules actually present into canonical order.
func orderModules(present map[string]bool) []string {
	var out []string
	for _, m := range moduleOrder {
		if present[m] {
			out = append(out, m)
			delete(present, m)
		}
	}
	var rest []string
	for m := range present {
		rest = append(rest, m)
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// readRoles parses every roles/*/defaults/main.yml under the Pigsty home and
// keeps the best definition of each parameter.
func readRoles(pigstyHome string) ([]param, string, error) {
	base := strings.TrimSuffix(pigstyHome, "/") + "/roles"
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, "", fmt.Errorf("read roles dir: %w", err)
	}
	var roles []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(base + "/" + e.Name() + "/defaults/main.yml"); err == nil {
			roles = append(roles, e.Name())
		}
	}
	if len(roles) == 0 {
		return nil, "", fmt.Errorf("no roles/*/defaults/main.yml found under %s", base)
	}
	sort.Strings(roles)

	candidates := map[string][]param{}
	for _, role := range roles {
		path := base + "/" + role + "/defaults/main.yml"
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, "", fmt.Errorf("read %s: %w", path, err)
		}
		for _, p := range parseRoleDefaults(role, data) {
			candidates[p.Key] = append(candidates[p.Key], p)
		}
	}

	keys := make([]string, 0, len(candidates))
	for k := range candidates {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	params := make([]param, 0, len(keys))
	version := ""
	for _, k := range keys {
		list := candidates[k]
		sort.SliceStable(list, func(i, j int) bool {
			ri, rj := ownerRank(list[i]), ownerRank(list[j])
			if ri != rj {
				return ri < rj
			}
			return list[i].Role < list[j].Role
		})
		best := list[0]
		if k == "version" && best.HasDefault {
			if s, ok := best.Default.(string); ok {
				version = s
			}
		}
		params = append(params, best)
	}
	return params, version, nil
}
