// Command schemagen generates schema/v<version>.json from the Pigsty role
// defaults plus hand written overrides.
//
//	go run ./tools/schemagen --pigsty-home .. --out schema/v4.5.0.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dbstd2333/pigsty-ui/console/internal/schema"
	"go.yaml.in/yaml/v3"
)

// overrides mirrors schema/overrides.yaml.
type overrides struct {
	Version string                   `yaml:"version"`
	Params  map[string]paramOverride `yaml:"params"`
	Defs    map[string]any           `yaml:"defs"`
}

// paramOverride lists the schema fields that hand written overrides may set.
type paramOverride struct {
	Module    *string `yaml:"module"`
	Section   *string `yaml:"section"`
	Role      *string `yaml:"role"`
	Level     *string `yaml:"level"`
	Type      *string `yaml:"type"`
	Default   *any    `yaml:"default"`
	Enum      *[]any  `yaml:"enum"`
	Identity  *bool   `yaml:"identity"`
	Sensitive *bool   `yaml:"sensitive"`
	Impact    *string `yaml:"impact"`
	Desc      *string `yaml:"desc"`
	Label     *string `yaml:"label"`
	Doc       *string `yaml:"doc"`
	Items     *any    `yaml:"items"`
	Ref       *string `yaml:"$ref"`
}

func main() {
	pigstyHome := flag.String("pigsty-home", ".", "path of the Pigsty installation (PIGSTY_HOME)")
	out := flag.String("out", "", "output file, default schema/<version>.json")
	overridesPath := flag.String("overrides", "", "overrides file, default <out dir>/overrides.yaml")
	flag.Parse()

	if err := run(*pigstyHome, *out, *overridesPath); err != nil {
		fmt.Fprintln(os.Stderr, "schemagen:", err)
		os.Exit(1)
	}
}

// run generates the schema file and prints a summary.
func run(pigstyHome, out, overridesPath string) error {
	params, roleVersion, err := readRoles(pigstyHome)
	if err != nil {
		return err
	}
	if overridesPath == "" {
		overridesPath = defaultOverridesPath(out)
	}
	ov, err := readOverrides(overridesPath)
	if err != nil {
		return err
	}
	version := roleVersion
	if ov.Version != "" {
		version = ov.Version
	}
	if version == "" {
		return fmt.Errorf("cannot determine Pigsty version: no version found in roles defaults")
	}
	if out == "" {
		out = filepath.Join("schema", version+".json")
	}

	doc := buildSchema(params, ov, version)
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal schema: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", out, err)
	}
	fmt.Printf("schemagen: %s %d params %d defs %d modules -> %s\n",
		version, len(doc.Params), len(doc.Defs), len(doc.Modules), out)
	return nil
}

// defaultOverridesPath prefers overrides.yaml next to the output file and
// falls back to schema/overrides.yaml in the working directory.
func defaultOverridesPath(out string) string {
	const fallback = "schema/overrides.yaml"
	if out == "" {
		return fallback
	}
	candidate := filepath.Join(filepath.Dir(out), "overrides.yaml")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return fallback
}

// readOverrides loads the hand written override document.
func readOverrides(path string) (*overrides, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read overrides %s: %w", path, err)
	}
	var ov overrides
	if err := yaml.Unmarshal(data, &ov); err != nil {
		return nil, fmt.Errorf("parse overrides %s: %w", path, err)
	}
	if ov.Params == nil {
		ov.Params = map[string]paramOverride{}
	}
	if ov.Defs == nil {
		ov.Defs = map[string]any{}
	}
	return &ov, nil
}

// buildSchema merges parsed role defaults with the overrides document.
func buildSchema(params []param, ov *overrides, version string) *schema.File {
	out := &schema.File{
		Version: version,
		Params:  make(map[string]schema.Param, len(params)+len(ov.Params)),
		Defs:    ov.Defs,
	}
	present := map[string]bool{}
	for _, p := range params {
		module := moduleOf(p.Role)
		sp := schema.Param{
			Module:  module,
			Section: p.Section,
			Role:    p.Role,
			Level:   p.Level,
			Type:    p.Type,
			Desc:    p.Desc,
		}
		if p.HasDefault {
			raw, err := json.Marshal(p.Default)
			if err == nil {
				sp.Default = raw
			}
		}
		if o, ok := ov.Params[p.Key]; ok {
			applyOverride(&sp, o)
		}
		if sp.Module == "" {
			sp.Module = module
		}
		if sp.Type == "" {
			sp.Type = "any"
		}
		out.Params[p.Key] = sp
		present[sp.Module] = true
	}
	// Overrides may introduce parameters that no role defines.
	for key, o := range ov.Params {
		if _, ok := out.Params[key]; ok {
			continue
		}
		sp := schema.Param{Module: "OTHER", Type: "any"}
		if o.Module == nil {
			fmt.Fprintf(os.Stderr, "schemagen: warning: override for unknown param %q without module\n", key)
		}
		applyOverride(&sp, o)
		out.Params[key] = sp
		present[sp.Module] = true
	}
	out.Modules = orderModules(present)
	return out
}

// applyOverride copies every non-empty override field onto a parameter.
func applyOverride(sp *schema.Param, o paramOverride) {
	setString := func(dst *string, src *string) {
		if src != nil {
			*dst = *src
		}
	}
	setBool := func(dst *bool, src *bool) {
		if src != nil {
			*dst = *src
		}
	}
	setString(&sp.Module, o.Module)
	setString(&sp.Section, o.Section)
	setString(&sp.Role, o.Role)
	setString(&sp.Level, o.Level)
	setString(&sp.Type, o.Type)
	setString(&sp.Impact, o.Impact)
	setString(&sp.Desc, o.Desc)
	setString(&sp.Label, o.Label)
	setString(&sp.Doc, o.Doc)
	setString(&sp.Ref, o.Ref)
	setBool(&sp.Identity, o.Identity)
	setBool(&sp.Sensitive, o.Sensitive)
	if o.Default != nil {
		if raw, err := json.Marshal(*o.Default); err == nil {
			sp.Default = raw
		}
	}
	if o.Enum != nil {
		sp.Enum = *o.Enum
	}
	if o.Items != nil {
		sp.Items = *o.Items
	}
}
