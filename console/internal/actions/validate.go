package actions

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/dbstd2333/pigsty-ui/console/internal/inventory"
	"github.com/dbstd2333/pigsty-ui/console/internal/plan"
	"github.com/dbstd2333/pigsty-ui/console/internal/validate"
)

// namePattern is the whitelist for cluster, user, database and object names.
var namePattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

// maxNameLen bounds every whitelisted identifier.
const maxNameLen = 64

// ParamError reports rejected or unresolvable parameters; the HTTP layer turns
// it into a 422 problem+json response carrying `issues`.
type ParamError struct {
	Issues []validate.Issue
	Detail string
}

// Error implements the error interface.
func (e *ParamError) Error() string {
	if len(e.Issues) > 0 {
		return e.Issues[0].Message
	}
	if e.Detail != "" {
		return e.Detail
	}
	return "参数校验失败"
}

// errNoPrimary reports that the primary of a cluster cannot be located.
var errNoPrimary = errors.New("primary not found")

// issue adds a parameter issue.
func (e *ParamError) issue(param, msg string) {
	e.Issues = append(e.Issues, validate.Issue{Level: "error", Source: "semantic", Message: msg, Path: param})
}

// resolved holds the validated parameters of one plan request plus the
// inventory facts needed to render the commands.
type resolved struct {
	action *action
	model  *inventory.Model

	cluster   string             // PG cluster of the action (empty when the action has none)
	info      *inventory.Cluster // inventory facts of that cluster
	source    string
	user      string
	db        string
	mode      string
	name      string
	ips       []string
	member    string
	leader    string
	candidate string

	settings   map[string]any
	target     any
	pitrAction string
	pitrBackup bool
	params     map[string]any // validated params with defaults applied
	primaryIP  string
	risk       plan.Risk
}

// outcome is the raw result of parameter resolution.
type outcome struct {
	res    *resolved
	errs   *ParamError
	extras []string // unchecked parameter names, reported as issues
}

// resolveParams validates the submitted parameters against the inventory.
func (p *Planner) resolveParams(a *action, params map[string]any) *outcome {
	out := &outcome{res: &resolved{action: a, params: map[string]any{}}, errs: &ParamError{}}
	res := out.res

	model, err := p.model()
	if err != nil {
		out.errs.issue("", "无法解析当前 pigsty.yml："+err.Error())
		return out
	}
	res.model = model

	for _, def := range a.def.Params {
		raw, present := params[def.Name]
		if !present || isBlank(raw) {
			if def.Default != nil {
				res.params[def.Name] = def.Default
				raw = def.Default
			} else if def.Required {
				out.errs.issue(def.Name, def.Label+"为必填参数")
				continue
			} else {
				continue
			}
		}
		value, err := p.validateParam(model, a, def, res.params, raw)
		if err != nil {
			out.errs.issue(def.Name, err.Error())
			continue
		}
		res.params[def.Name] = value
	}

	// Unknown parameters are rejected so clients cannot smuggle extra vars.
	for name := range params {
		if a.hasParam(name) {
			continue
		}
		if p.Simulate && name == "fail" {
			res.params["fail"] = params[name]
			continue
		}
		out.errs.issue(name, "动作 "+a.def.ID+" 不支持参数 "+name)
	}

	if len(out.errs.Issues) > 0 {
		return out
	}
	p.assign(res)
	out.extras = nil
	return out
}

// assign copies the validated parameters into the typed fields of the plan.
func (p *Planner) assign(res *resolved) {
	str := func(name string) string {
		v, _ := res.params[name].(string)
		return v
	}
	if res.params["cluster"] != nil {
		if c, ok := res.model.Cluster(inventory.ModulePGSQL, str("cluster")); ok {
			res.cluster, res.info = c.Identity, &c
		}
	}
	if res.params["cluster"] != nil && res.action.def.Category == "redis" {
		if c, ok := res.model.Cluster(inventory.ModuleRedis, str("cluster")); ok {
			res.cluster, res.info = c.Identity, &c
		}
	}
	res.source = str("source")
	res.user = str("user")
	res.db = str("db")
	res.mode = str("mode")
	res.name = str("name")
	res.member = str("member")
	res.leader = str("leader")
	res.candidate = str("candidate")
	res.pitrAction = str("action")
	if res.pitrAction == "" {
		res.pitrAction = "pause"
	}
	if v, ok := res.params["backup"].(bool); ok {
		res.pitrBackup = v
	}
	if v, ok := res.params["settings"].(map[string]any); ok {
		res.settings = v
	}
	res.target = res.params["target"]
	if ips, ok := res.params["ips"].([]string); ok {
		res.ips = ips
	}
}

// validateParam checks one parameter value against the inventory and the
// declared type, returning the normalized value.
func (p *Planner) validateParam(model *inventory.Model, a *action, def ActionParam, params map[string]any, raw any) (any, error) {
	switch def.Type {
	case "pg_cluster", "redis_cluster":
		val, err := whitelist(def.Label, raw, patternOf(a, def))
		if err != nil {
			return nil, err
		}
		module := inventory.ModulePGSQL
		if def.Type == "redis_cluster" {
			module = inventory.ModuleRedis
		}
		if _, ok := model.Cluster(module, val); !ok {
			return nil, fmt.Errorf("inventory 中没有 %s 集群 %q", module, val)
		}
		return val, nil

	case "pg_member", "ip_list":
		values, err := ipValues(def.Label, raw)
		if err != nil {
			return nil, err
		}
		if def.Of != nil {
			cluster, ok := model.Cluster(inventory.ModulePGSQL, ofValue(params, *def.Of))
			if !ok {
				return nil, fmt.Errorf("无法定位参数 %s 指定的集群", *def.Of)
			}
			hosts := map[string]bool{}
			for _, h := range cluster.Hosts {
				hosts[h] = true
			}
			for _, ip := range values {
				if !hosts[ip] {
					return nil, fmt.Errorf("%s 不属于集群 %s", ip, cluster.Identity)
				}
			}
		}
		if def.Type == "pg_member" {
			return values[0], nil
		}
		return values, nil

	case "pg_user", "pg_db":
		val, err := whitelist(def.Label, raw, patternOf(a, def))
		if err != nil {
			return nil, err
		}
		cluster, ok := model.Cluster(inventory.ModulePGSQL, ofValue(params, derefOf(def.Of)))
		if !ok {
			return nil, fmt.Errorf("无法定位参数 %s 指定的集群", derefOf(def.Of))
		}
		key := "pg_users"
		if def.Type == "pg_db" {
			key = "pg_databases"
		}
		names := entryNames(cluster.Vars, key)
		for _, n := range names {
			if n == val {
				return val, nil
			}
		}
		if len(names) == 0 {
			return nil, fmt.Errorf("集群 %s 的 %s 中没有任何定义", cluster.Identity, key)
		}
		return nil, fmt.Errorf("集群 %s 的 %s 中没有 %q（可选：%s）", cluster.Identity, key, val, strings.Join(names, ", "))

	case "enum":
		val := fmt.Sprint(raw)
		for _, item := range def.Enum {
			if item == val {
				return val, nil
			}
		}
		return nil, fmt.Errorf("%s 只能是 %s", def.Label, strings.Join(def.Enum, "/"))

	case "bool":
		switch v := raw.(type) {
		case bool:
			return v, nil
		case string:
			b, err := strconv.ParseBool(strings.TrimSpace(v))
			if err == nil {
				return b, nil
			}
		}
		return nil, fmt.Errorf("%s 需为布尔值", def.Label)

	case "int":
		switch v := raw.(type) {
		case float64:
			return int(v), nil
		case int:
			return v, nil
		case string:
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err == nil {
				return n, nil
			}
		}
		return nil, fmt.Errorf("%s 需为整数", def.Label)

	case "string":
		pattern := patternOf(a, def)
		val, err := whitelist(def.Label, raw, pattern)
		if err != nil {
			return nil, err
		}
		return val, nil

	case "datetime":
		val, err := whitelist(def.Label, raw, `^[0-9T: +.\-]{10,40}$`)
		if err != nil {
			return nil, err
		}
		t, err := plan.ParsePitrTime(val)
		if err != nil {
			return nil, err
		}
		return plan.FormatPitrTime(t), nil

	case "object":
		obj, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s 需为对象", def.Label)
		}
		if def.Name == "settings" {
			settings, err := validateSettings(obj)
			if err != nil {
				return nil, fmt.Errorf("%s：%w", def.Label, err)
			}
			return settings, nil
		}
		return obj, nil
	}
	return nil, fmt.Errorf("参数 %s 的类型 %s 未知", def.Name, def.Type)
}

// validateSettings checks a patronictl edit-config payload.
func validateSettings(obj map[string]any) (map[string]any, error) {
	if len(obj) == 0 {
		return nil, fmt.Errorf("参数不能为空")
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !namePattern.MatchString(k) || len(k) > maxNameLen {
			return nil, fmt.Errorf("参数名 %q 非法", k)
		}
		switch v := obj[k].(type) {
		case string, bool, float64, int:
			if s, ok := v.(string); ok && (strings.ContainsAny(s, "\n\r\x00") || len(s) > 256) {
				return nil, fmt.Errorf("参数 %s 的值非法", k)
			}
		default:
			return nil, fmt.Errorf("参数 %s 的值需为字符串、数字或布尔值", k)
		}
	}
	return obj, nil
}

// whitelist validates a scalar against a whitelist pattern.
func whitelist(label string, raw any, pattern string) (string, error) {
	var val string
	switch v := raw.(type) {
	case string:
		val = v
	case float64:
		val = strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		val = strconv.FormatBool(v)
	default:
		return "", fmt.Errorf("%s 需为字符串", label)
	}
	val = strings.TrimSpace(val)
	if val == "" {
		return "", fmt.Errorf("%s 不能为空", label)
	}
	re := namePattern
	if pattern != "" {
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return "", fmt.Errorf("内置校验规则非法: %w", err)
		}
		re = compiled
	}
	if !re.MatchString(val) {
		return "", fmt.Errorf("%s %q 含非法字符（允许 %s）", label, val, re.String())
	}
	if len(val) > maxNameLen {
		return "", fmt.Errorf("%s 过长（最多 %d 字符）", label, maxNameLen)
	}
	return val, nil
}

// patternOf returns the whitelist pattern declared for a parameter.
func patternOf(a *action, def ActionParam) string {
	if p := Pattern(a.def.ID, def.Name); p != "" {
		return p
	}
	return ""
}

// ipValues normalizes an IP or list of IPs and validates them as IPv4.
func ipValues(label string, raw any) ([]string, error) {
	var values []string
	switch v := raw.(type) {
	case string:
		values = []string{v}
	case []string:
		values = append(values, v...)
	case []any:
		for _, item := range v {
			values = append(values, fmt.Sprint(item))
		}
	default:
		return nil, fmt.Errorf("%s 需为 IP 或 IP 列表", label)
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("%s 不能为空", label)
	}
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, v := range values {
		ip := strings.TrimSpace(v)
		if parsed := net.ParseIP(ip); parsed == nil || parsed.To4() == nil {
			return nil, fmt.Errorf("%q 不是合法的 IPv4 地址", ip)
		}
		if seen[ip] {
			continue
		}
		seen[ip] = true
		out = append(out, ip)
	}
	return out, nil
}

// entryNames extracts the `name` field of a list of inventory definitions.
func entryNames(vars map[string]any, key string) []string {
	list, _ := vars[key].([]any)
	out := make([]string, 0, len(list))
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if name, ok := entry["name"].(string); ok && name != "" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// entryState returns the `state` field of one inventory definition.
func entryState(vars map[string]any, key, name string) string {
	list, _ := vars[key].([]any)
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if fmt.Sprint(entry["name"]) != name {
			continue
		}
		if state, ok := entry["state"].(string); ok && state != "" {
			return state
		}
		return "create"
	}
	return ""
}

// derefOf dereferences an optional `of` pointer.
func derefOf(of *string) string {
	if of == nil {
		return ""
	}
	return *of
}

// ofValue resolves a parameter that another parameter points at.
func ofValue(params map[string]any, of string) string {
	if of == "" {
		return ""
	}
	if v, ok := params[of].(string); ok {
		return v
	}
	return ""
}

// isBlank reports whether a submitted value counts as missing.
func isBlank(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(t) == ""
	case []string:
		return len(t) == 0
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	default:
		return false
	}
}
