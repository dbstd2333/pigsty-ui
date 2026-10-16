// Package actions owns the console action catalog: the embedded whitelist of
// playbook/ansible ad-hoc actions, the parameter validation against the current
// inventory, the precondition checks and the command rendering.
package actions

import (
	_ "embed"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/dbstd2333/pigsty-ui/console/internal/plan"
)

//go:embed catalog.yaml
var catalogYAML []byte

// ActionParam is one parameter of an action (see console/API-M2-M5.md).
type ActionParam struct {
	Name     string   `json:"name"`
	Label    string   `json:"label"`
	Type     string   `json:"type" enum:"pg_cluster,redis_cluster,pg_member,ip_list,pg_user,pg_db,enum,string,bool,int,datetime,object"`
	Of       *string  `json:"of"`
	Required bool     `json:"required"`
	Enum     []string `json:"enum"`
	Default  any      `json:"default"`
	Help     *string  `json:"help"`
}

// ActionDef is one entry of GET /actions.
type ActionDef struct {
	ID          string        `json:"id"`
	Title       string        `json:"title"`
	Description string        `json:"description"`
	Category    string        `json:"category" enum:"pgsql,node,redis,backup,patroni"`
	Risk        plan.Risk     `json:"risk" enum:"readonly,low,medium,high,critical"`
	Check       bool          `json:"check"`
	Gate        bool          `json:"gate"`
	Params      []ActionParam `json:"params"`
}

// Step is one command of an action, rendered from catalog.yaml templates.
type Step struct {
	Cmd        string            `yaml:"cmd"` // playbook | adhoc | pitr
	Playbook   string            `yaml:"playbook"`
	Limit      string            `yaml:"limit"`
	Tags       []string          `yaml:"tags"`
	Vars       map[string]string `yaml:"vars"`
	Host       string            `yaml:"host"` // primary | ips
	BecomeUser string            `yaml:"become_user"`
	Command    string            `yaml:"command"`
	Cluster    string            `yaml:"cluster"`
	Source     string            `yaml:"source"`
}

// action is one catalog entry: the public definition plus its execution steps.
type action struct {
	def           ActionDef
	Steps         []Step   `yaml:"steps"`
	Preconditions []string `yaml:"preconditions"`
}

// Catalog is the parsed, immutable action whitelist.
type Catalog struct {
	order []string
	byID  map[string]*action
}

// Load parses the embedded action catalog.
func Load() (*Catalog, error) {
	var raw []struct {
		ID            string   `yaml:"id"`
		Title         string   `yaml:"title"`
		Description   string   `yaml:"description"`
		Category      string   `yaml:"category"`
		Risk          string   `yaml:"risk"`
		Check         bool     `yaml:"check"`
		Gate          bool     `yaml:"gate"`
		Preconditions []string `yaml:"preconditions"`
		Params        []struct {
			Name     string   `yaml:"name"`
			Label    string   `yaml:"label"`
			Type     string   `yaml:"type"`
			Of       string   `yaml:"of"`
			Required bool     `yaml:"required"`
			Enum     []string `yaml:"enum"`
			Default  any      `yaml:"default"`
			Help     string   `yaml:"help"`
			Pattern  string   `yaml:"pattern"`
		} `yaml:"params"`
		Steps []Step `yaml:"steps"`
	}
	if err := yaml.Unmarshal(catalogYAML, &raw); err != nil {
		return nil, fmt.Errorf("parse action catalog: %w", err)
	}
	c := &Catalog{byID: make(map[string]*action, len(raw))}
	for _, r := range raw {
		if r.ID == "" || len(r.Steps) == 0 {
			return nil, fmt.Errorf("action catalog entry %q is incomplete", r.ID)
		}
		if !plan.Risk(r.Risk).Valid() {
			return nil, fmt.Errorf("action %s has invalid risk %q", r.ID, r.Risk)
		}
		if _, dup := c.byID[r.ID]; dup {
			return nil, fmt.Errorf("duplicate action id %q", r.ID)
		}
		def := ActionDef{
			ID:          r.ID,
			Title:       r.Title,
			Description: r.Description,
			Category:    r.Category,
			Risk:        plan.Risk(r.Risk),
			Check:       r.Check,
			Gate:        r.Gate,
			Params:      make([]ActionParam, 0, len(r.Params)),
		}
		for _, p := range r.Params {
			ap := ActionParam{
				Name:     p.Name,
				Label:    p.Label,
				Type:     p.Type,
				Required: p.Required,
				Enum:     append([]string{}, p.Enum...),
				Default:  p.Default,
			}
			if p.Of != "" {
				of := p.Of
				ap.Of = &of
			}
			if p.Help != "" {
				help := p.Help
				ap.Help = &help
			}
			def.Params = append(def.Params, ap)
			paramPatterns[r.ID+"."+p.Name] = p.Pattern
		}
		c.byID[r.ID] = &action{def: def, Steps: r.Steps, Preconditions: r.Preconditions}
		c.order = append(c.order, r.ID)
	}
	return c, nil
}

// paramPatterns holds optional per-parameter whitelists declared in the catalog.
var paramPatterns = map[string]string{}

// Pattern returns the declared whitelist of one parameter, empty when none.
func Pattern(actionID, param string) string { return paramPatterns[actionID+"."+param] }

// List returns the action definitions in catalog order.
func (c *Catalog) List() []ActionDef {
	out := make([]ActionDef, 0, len(c.order))
	for _, id := range c.order {
		out = append(out, c.byID[id].def)
	}
	return out
}

// Get returns one action definition.
func (c *Catalog) Get(id string) (ActionDef, bool) {
	a, ok := c.byID[id]
	if !ok {
		return ActionDef{}, false
	}
	return a.def, true
}

// IDs returns the action ids in catalog order.
func (c *Catalog) IDs() []string { return append([]string{}, c.order...) }

// lookup returns the internal catalog entry of an action.
func (c *Catalog) lookup(id string) (*action, error) {
	a, ok := c.byID[id]
	if !ok {
		return nil, fmt.Errorf("unknown action %q", id)
	}
	return a, nil
}

// paramDef returns the definition of one parameter of an action.
func (a *action) paramDef(name string) (ActionParam, bool) {
	for _, p := range a.def.Params {
		if p.Name == name {
			return p, true
		}
	}
	return ActionParam{}, false
}

// hasParam reports whether an action declares a parameter.
func (a *action) hasParam(name string) bool {
	_, ok := a.paramDef(name)
	return ok
}

// title renders the human readable job title: action title plus the key
// parameter of the action, e.g. 「创建/更新业务用户 dbuser_app @ pg-test」.
func (a *action) title(params map[string]any) string {
	var key string
	switch {
	case params["user"] != nil:
		key = fmt.Sprint(params["user"])
	case params["db"] != nil:
		key = fmt.Sprint(params["db"])
	case params["name"] != nil:
		key = fmt.Sprint(params["name"])
	case params["ips"] != nil:
		key = joinAny(params["ips"])
	}
	title := a.def.Title
	if key == "" {
		return title
	}
	cluster, _ := params["cluster"].(string)
	if cluster == "" {
		return title + " " + key
	}
	return fmt.Sprintf("%s %s @ %s", title, key, cluster)
}

// joinAny renders a string slice or single value for titles.
func joinAny(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			parts = append(parts, fmt.Sprint(item))
		}
		return strings.Join(parts, ",")
	case []string:
		return strings.Join(t, ",")
	default:
		return fmt.Sprint(v)
	}
}
