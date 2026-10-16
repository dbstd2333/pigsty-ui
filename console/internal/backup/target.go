// Package backup parses pgBackRest repository information, plans PITR runs and
// renders the pg_pitr extra vars consumed by pgsql-pitr.yml.
package backup

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/dbstd2333/pigsty-ui/console/internal/plan"
)

// PitrTarget is the recovery target submitted by the PITR wizard.
type PitrTarget struct {
	Type      string `json:"type" enum:"latest,time,name,lsn,xid,immediate"`
	Time      string `json:"time,omitempty"`
	Name      string `json:"name,omitempty"`
	LSN       string `json:"lsn,omitempty"`
	XID       string `json:"xid,omitempty"`
	Set       string `json:"set,omitempty"`
	Exclusive bool   `json:"exclusive,omitempty"`
}

// RawTarget is the untyped form of a submitted target.
type RawTarget map[string]any

var (
	lsnPattern  = regexp.MustCompile(`^[0-9A-Fa-f]{1,8}/[0-9A-Fa-f]{1,8}$`)
	namePattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,64}$`)
)

// TargetTypes lists the accepted target types.
func TargetTypes() []string { return []string{"latest", "time", "name", "lsn", "xid", "immediate"} }

// ParseTarget validates an untyped target object.
func ParseTarget(raw RawTarget) (PitrTarget, error) {
	t := PitrTarget{}
	if raw == nil {
		return t, fmt.Errorf("恢复目标不能为空")
	}
	t.Type = strings.TrimSpace(fmt.Sprint(raw["type"]))
	if t.Type == "" || t.Type == "<nil>" {
		t.Type = "latest"
	}
	valid := false
	for _, item := range TargetTypes() {
		if item == t.Type {
			valid = true
		}
	}
	if !valid {
		return t, fmt.Errorf("恢复目标类型只能是 %s", strings.Join(TargetTypes(), "/"))
	}
	t.Time = str(raw["time"])
	t.Name = str(raw["name"])
	t.LSN = strings.ToUpper(str(raw["lsn"]))
	t.XID = str(raw["xid"])
	t.Set = str(raw["set"])
	if v, ok := raw["exclusive"].(bool); ok {
		t.Exclusive = v
	}

	switch t.Type {
	case "time":
		if t.Time == "" {
			return t, fmt.Errorf("恢复目标类型 time 需要提供 time")
		}
		ts, err := plan.ParsePitrTime(t.Time)
		if err != nil {
			return t, err
		}
		t.Time = plan.FormatPitrTime(ts)
	case "name":
		if !namePattern.MatchString(t.Name) {
			return t, fmt.Errorf("恢复点名称 %q 非法（允许字母数字与 _.-）", t.Name)
		}
	case "lsn":
		if !lsnPattern.MatchString(t.LSN) {
			return t, fmt.Errorf("LSN %q 格式非法，需形如 0/3000000", t.LSN)
		}
	case "xid":
		n, err := strconv.ParseInt(t.XID, 10, 64)
		if err != nil || n <= 0 || n >= 4294967296 {
			return t, fmt.Errorf("XID %q 非法，需为 32 位正整数", t.XID)
		}
	}
	if t.Set != "" && !namePattern.MatchString(t.Set) {
		return t, fmt.Errorf("备份集 %q 非法", t.Set)
	}
	return t, nil
}

func str(v any) string {
	if v == nil {
		return ""
	}
	s := strings.TrimSpace(fmt.Sprint(v))
	if s == "<nil>" {
		return ""
	}
	return s
}

// PgPitr converts a validated target into the pg_pitr extra vars of
// pgsql-pitr.yml. The `latest` type is rendered by omitting the type key, which
// the playbook reads as "restore to the end of the WAL archive stream".
func PgPitr(t PitrTarget, action string, backupExisting bool, source string) map[string]any {
	out := map[string]any{}
	switch t.Type {
	case "time":
		out["type"] = "time"
		out["time"] = t.Time
	case "name":
		out["type"] = "name"
		out["name"] = t.Name
	case "lsn":
		out["type"] = "lsn"
		out["lsn"] = strings.ToUpper(t.LSN)
	case "xid":
		out["type"] = "xid"
		out["xid"] = t.XID
	case "immediate":
		out["type"] = "immediate"
	}
	if t.Set != "" && t.Set != "latest" {
		out["set"] = t.Set
	}
	if t.Exclusive {
		out["exclusive"] = true
	}
	if action != "" {
		out["action"] = action
	}
	if backupExisting {
		out["backup"] = true
	}
	if source != "" {
		out["cluster"] = source
	}
	return out
}

// PgPitrJSON renders the pg_pitr payload as compact JSON, with sorted keys so
// the rendered command is stable.
func PgPitrJSON(pgPitr map[string]any) string {
	keys := make([]string, 0, len(pgPitr))
	for k := range pgPitr {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("{")
	for i, k := range keys {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(quoteJSON(k))
		b.WriteString(":")
		b.WriteString(valueJSON(pgPitr[k]))
	}
	b.WriteString("}")
	return b.String()
}

func quoteJSON(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func valueJSON(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case string:
		return quoteJSON(t)
	case bool:
		if t {
			return "true"
		}
		return "false"
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case map[string]any:
		return PgPitrJSON(t)
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			parts = append(parts, valueJSON(item))
		}
		return "[" + strings.Join(parts, ",") + "]"
	default:
		return quoteJSON(fmt.Sprint(t))
	}
}
