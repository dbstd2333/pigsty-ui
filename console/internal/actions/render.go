package actions

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"

	"github.com/dbstd2333/pigsty-ui/console/internal/backup"
	"github.com/dbstd2333/pigsty-ui/console/internal/inventory"
)

// Command is one rendered step of an action: the exact argv to execute, the
// single line form shown in the UI, and the metadata the job runner needs.
type Command struct {
	Argv     []string `json:"argv"`
	Display  string   `json:"display"`
	Kind     string   `json:"kind"` // playbook | adhoc | pitr
	Playbook string   `json:"playbook"`
	Hosts    []string `json:"hosts"`
}

// placeholderPattern matches catalog template variables like {{ cluster }}.
var placeholderPattern = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_]+)\s*\}\}`)

// renderTemplate substitutes whitelisted placeholders; unknown ones are errors
// so a typo in catalog.yaml fails loudly instead of leaking into a command.
func renderTemplate(tmpl string, vars map[string]string) (string, error) {
	var firstErr error
	out := placeholderPattern.ReplaceAllStringFunc(tmpl, func(match string) string {
		name := placeholderPattern.FindStringSubmatch(match)[1]
		value, ok := vars[name]
		if !ok {
			if firstErr == nil {
				firstErr = fmt.Errorf("未知的模板变量 %s", name)
			}
			return match
		}
		return value
	})
	if firstErr != nil {
		return "", firstErr
	}
	return out, nil
}

// renderStep renders one catalog step into an executable command.
func (p *Planner) renderStep(ctx context.Context, res *resolved, st Step, check bool) (Command, error) {
	switch st.Cmd {
	case "playbook":
		vars, err := p.stepVars(ctx, res, st.Cmd == "adhoc")
		if err != nil {
			return Command{}, err
		}
		return p.renderPlaybook(res, st, vars, check)
	case "adhoc":
		vars, err := p.stepVars(ctx, res, true)
		if err != nil {
			return Command{}, err
		}
		return p.renderAdhoc(ctx, res, st, vars)
	case "pitr":
		return p.renderPitr(ctx, res, st)
	default:
		return Command{}, fmt.Errorf("动作 %s 的步骤类型 %q 未知", res.action.def.ID, st.Cmd)
	}
}

// renderPlaybook builds ./<playbook> -l <limit> [-t tags] [-e k=v] [--check].
func (p *Planner) renderPlaybook(res *resolved, st Step, vars map[string]string, check bool) (Command, error) {
	limit, err := renderTemplate(st.Limit, vars)
	if err != nil {
		return Command{}, err
	}
	if strings.TrimSpace(limit) == "" {
		return Command{}, fmt.Errorf("动作 %s 的步骤缺少 -l 目标", res.action.def.ID)
	}
	argv := []string{"./" + st.Playbook, "-l", limit}
	if len(st.Tags) > 0 {
		argv = append(argv, "-t", strings.Join(st.Tags, ","))
	}
	if check && res.action.def.Check {
		argv = append(argv, "--check")
	}
	names := make([]string, 0, len(st.Vars))
	for name := range st.Vars {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value, err := renderTemplate(st.Vars[name], vars)
		if err != nil {
			return Command{}, err
		}
		argv = append(argv, "-e", name+"="+value)
	}
	if err := AssertArgv(argv); err != nil {
		return Command{}, err
	}
	return Command{Argv: argv, Display: DisplayArgv(argv), Kind: "playbook", Playbook: st.Playbook, Hosts: limitHosts(limit, res)}, nil
}

// renderAdhoc builds the ansible ad-hoc wrapper around one remote command.
func (p *Planner) renderAdhoc(ctx context.Context, res *resolved, st Step, vars map[string]string) (Command, error) {
	remote, err := renderTemplate(st.Command, vars)
	if err != nil {
		return Command{}, err
	}
	host, err := p.stepHost(ctx, res, st.Host)
	if err != nil {
		return Command{}, err
	}
	dbsu := "postgres"
	if v, ok := vars["dbsu"]; ok && v != "" {
		dbsu = v
	}
	argv := []string{"ansible", host, "-i", "pigsty.yml", "-b", "--become-user=" + dbsu, "-m", "shell", "-a", remote}
	if err := AssertArgv(argv); err != nil {
		return Command{}, err
	}
	return Command{Argv: argv, Display: DisplayArgv(argv), Kind: "adhoc", Hosts: []string{host}}, nil
}

// renderPitr renders the pgsql-pitr.yml invocation with the pg_pitr payload.
func (p *Planner) renderPitr(_ context.Context, res *resolved, st Step) (Command, error) {
	target, err := backup.ParseTarget(toRawMap(res.target))
	if err != nil {
		return Command{}, err
	}
	source := ""
	if st.Source != "" {
		source, err = renderTemplate(st.Source, map[string]string{"source": res.source, "cluster": res.cluster})
		if err != nil {
			return Command{}, err
		}
		if c, ok := res.model.Cluster(inventory.ModulePGSQL, source); ok {
			source = c.Identity
		}
	}
	cluster, err := renderTemplate(st.Cluster, map[string]string{"cluster": res.cluster})
	if err != nil {
		return Command{}, err
	}
	payload := map[string]any{"pg_pitr": backup.PgPitr(target, res.pitrAction, res.pitrBackup, source)}
	argv := []string{"./pgsql-pitr.yml", "-l", cluster, "-e", backup.PgPitrJSON(payload)}
	if err := AssertArgv(argv); err != nil {
		return Command{}, err
	}
	return Command{Argv: argv, Display: DisplayArgv(argv), Kind: "pitr", Playbook: "pgsql-pitr.yml",
		Hosts: limitHosts(cluster, res)}, nil
}

// stepVars builds the placeholder map of one step. The primary IP is only
// resolved when the step needs it, so a cluster without a reachable primary can
// still be planned for actions that do not touch it.
func (p *Planner) stepVars(ctx context.Context, res *resolved, needPrimary bool) (map[string]string, error) {
	primary := ""
	if needPrimary {
		ip, err := p.primaryIP(ctx, res)
		if err != nil {
			return nil, err
		}
		primary = ip
	}
	vars := map[string]string{
		"cluster":        res.cluster,
		"source":         res.source,
		"user":           res.user,
		"db":             res.db,
		"mode":           res.mode,
		"name":           res.name,
		"ips":            strings.Join(res.ips, ","),
		"settings_args":  settingsArgs(res.settings),
		"member_name":    p.memberName(res, res.member),
		"leader_name":    p.memberName(res, res.leader),
		"candidate_name": p.memberName(res, res.candidate),
		"dbsu":           p.dbsu(res),
		"primary":        primary,
	}
	return vars, nil
}

// stepHost resolves the ad-hoc target host of a step.
func (p *Planner) stepHost(ctx context.Context, res *resolved, spec string) (string, error) {
	switch spec {
	case "", "primary":
		ip, err := p.primaryIP(ctx, res)
		if err != nil {
			return "", err
		}
		return ip, nil
	case "ips":
		if len(res.ips) == 0 {
			return "", fmt.Errorf("动作 %s 需要至少一个目标 IP", res.action.def.ID)
		}
		return res.ips[0], nil
	default:
		return renderTemplate(spec, map[string]string{"ip": firstIP(res.ips)})
	}
}

// primaryIP returns the primary IP of the target cluster, preferring the
// Patroni leader and falling back to the inventory pg_role.
func (p *Planner) primaryIP(ctx context.Context, res *resolved) (string, error) {
	if res.primaryIP != "" {
		return res.primaryIP, nil
	}
	if res.info == nil {
		return "", fmt.Errorf("%w: 动作缺少集群信息", errNoPrimary)
	}
	if p.Facts != nil {
		if members, ok := p.Facts.PatroniMembers(ctx, res.cluster); ok {
			for _, m := range members {
				if m.Role == "leader" || m.Role == "standby_leader" {
					res.primaryIP = m.Host
					return m.Host, nil
				}
			}
		}
	}
	for _, host := range res.info.Hosts {
		if p.pgRole(res, host) == "primary" {
			res.primaryIP = host
			return host, nil
		}
	}
	return "", fmt.Errorf("%w: 无法确定集群 %s 的主库 IP，Patroni 不可达且 inventory 中没有 pg_role=primary 的实例", errNoPrimary, res.cluster)
}

// memberName maps an instance IP onto its Patroni member name.
func (p *Planner) memberName(res *resolved, ip string) string {
	if ip == "" {
		return ""
	}
	if res.info != nil {
		if hostVars, ok := res.model.HostVarsIn(res.info.Group, ip); ok {
			if seq, ok := hostVars["pg_seq"]; ok {
				return fmt.Sprintf("%s-%v", res.cluster, seq)
			}
		}
	}
	return ip
}

// dbsu returns the OS user that owns the PostgreSQL instances of a cluster.
func (p *Planner) dbsu(res *resolved) string {
	if res.info != nil {
		if v, ok := res.info.Vars["pg_dbsu"].(string); ok && v != "" {
			return v
		}
	}
	return "postgres"
}

// pgRole returns the pg_role declared in the inventory for one instance.
func (p *Planner) pgRole(res *resolved, host string) string {
	if res.info == nil {
		return ""
	}
	return res.info.Roles[host]
}

// limitHosts returns the hosts a playbook limit expands to.
func limitHosts(limit string, res *resolved) []string {
	if res.info != nil && limit == res.cluster {
		return append([]string{}, res.info.Hosts...)
	}
	return splitList(limit)
}

// settingsArgs renders `-s key=value` pairs for patronictl edit-config.
func settingsArgs(settings map[string]any) string {
	if len(settings) == 0 {
		return ""
	}
	keys := make([]string, 0, len(settings))
	for k := range settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("-s %s=%v", k, settings[k]))
	}
	return strings.Join(parts, " ")
}

// splitList splits a comma separated host list.
func splitList(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// toRawMap converts an untyped target parameter into the raw target map.
func toRawMap(v any) backup.RawTarget {
	if obj, ok := v.(map[string]any); ok {
		return backup.RawTarget(obj)
	}
	return nil
}

// firstIP returns the first IP of a list, empty when the list is empty.
func firstIP(ips []string) string {
	if len(ips) == 0 {
		return ""
	}
	return ips[0]
}

// DisplayArgv renders an argv array as one shell-like line for the UI. The
// result is display only: commands always run through the argv array.
func DisplayArgv(argv []string) string {
	parts := make([]string, 0, len(argv))
	for _, arg := range argv {
		parts = append(parts, quoteArg(arg))
	}
	return strings.Join(parts, " ")
}

// quoteArg quotes one argv element when it contains shell metacharacters.
func quoteArg(arg string) string {
	if arg == "" {
		return "''"
	}
	if !strings.ContainsAny(arg, " \t\n\"'\\$&|;<>()*?[]{}!") {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// AssertArgv enforces the two structural safety rules of the catalog: every
// playbook command carries an explicit `-l` limit, and every ad-hoc command
// targets one concrete host.
func AssertArgv(argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("空命令")
	}
	switch {
	case argv[0] == "ansible":
		if len(argv) < 2 {
			return fmt.Errorf("ansible ad-hoc 命令缺少目标主机")
		}
		host := argv[1]
		if net.ParseIP(host) == nil || net.ParseIP(host).To4() == nil {
			return fmt.Errorf("ansible ad-hoc 目标 %q 不是具体 IPv4 主机", host)
		}
	case strings.HasPrefix(argv[0], "./"):
		found := false
		for i, arg := range argv {
			if arg != "-l" {
				continue
			}
			if i+1 >= len(argv) || strings.TrimSpace(argv[i+1]) == "" || argv[i+1] == "all" {
				return fmt.Errorf("%s 的 -l 目标为空或 all", argv[0])
			}
			found = true
		}
		if !found {
			return fmt.Errorf("%s 缺少 -l 目标限制", argv[0])
		}
	default:
		return fmt.Errorf("未知的命令类型 %q", argv[0])
	}
	return nil
}
