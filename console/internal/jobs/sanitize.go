package jobs

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// sensitiveKeyPattern matches the inventory keys whose values are secrets, the
// same rule the parameter schema uses.
var sensitiveKeyPattern = regexp.MustCompile(`(?i)password|secret|_key|pass`)

// nonSecretTerms are values that match a sensitive key name but are product or
// protocol identifiers; masking them would mangle every path and playbook name
// in the output (e.g. `pigsty.yml`).
var nonSecretTerms = map[string]bool{
	"pigsty":   true,
	"postgres": true,
	"true":     true,
	"false":    true,
}

// minSecretLength keeps single character placeholder values out of the mask
// list, where they would destroy the readability of the output.
const minSecretLength = 6

// Sanitizer masks the values of sensitive inventory parameters in every line the
// console shows, streams or stores.
type Sanitizer struct {
	replacer *strings.Replacer
	values   []string
}

// NewSanitizer collects the secret values of one pigsty.yml document.
func NewSanitizer(text string) *Sanitizer {
	seen := map[string]bool{}
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			for key, value := range t {
				if sensitiveKeyPattern.MatchString(key) {
					for _, secret := range scalarSecrets(value) {
						seen[secret] = true
					}
				}
				walk(value)
			}
		case []any:
			for _, item := range t {
				walk(item)
			}
		}
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(text), &doc); err == nil {
		walk(doc)
	}
	delete(seen, "")
	for term := range nonSecretTerms {
		delete(seen, term)
	}
	values := make([]string, 0, len(seen))
	for value := range seen {
		if len(value) < minSecretLength {
			continue
		}
		values = append(values, value)
	}
	// Longest first so a secret that contains another one is replaced whole.
	sort.Slice(values, func(i, j int) bool {
		if len(values[i]) != len(values[j]) {
			return len(values[i]) > len(values[j])
		}
		return values[i] < values[j]
	})
	pairs := make([]string, 0, len(values)*4)
	for _, value := range values {
		pairs = append(pairs, value, mask)
		if escaped := strconv.Quote(value); escaped != `"`+value+`"` {
			pairs = append(pairs, strings.Trim(escaped, `"`), mask)
		}
	}
	return &Sanitizer{replacer: strings.NewReplacer(pairs...), values: values}
}

// mask is what replaces a secret value.
const mask = "***"

// scalarSecrets renders the secret values of one YAML node.
func scalarSecrets(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		out := []string{}
		for _, item := range t {
			out = append(out, scalarSecrets(item)...)
		}
		return out
	case map[string]any:
		out := []string{}
		for _, item := range t {
			out = append(out, scalarSecrets(item)...)
		}
		return out
	default:
		return nil
	}
}

// Text masks every known secret in a string.
func (s *Sanitizer) Text(v string) string {
	if s == nil || s.replacer == nil || v == "" {
		return v
	}
	return s.replacer.Replace(v)
}

// Bytes masks every known secret in a byte slice.
func (s *Sanitizer) Bytes(v []byte) []byte {
	if s == nil || s.replacer == nil || len(v) == 0 {
		return v
	}
	return []byte(s.replacer.Replace(string(v)))
}

// Count reports how many distinct secret values are masked.
func (s *Sanitizer) Count() int {
	if s == nil {
		return 0
	}
	return len(s.values)
}
