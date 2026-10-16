package inventory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sync"
)

// Ansible runs ansible-inventory when the binary is available.
type Ansible struct {
	binary string
}

// DetectAnsible probes PATH for ansible-inventory.
func DetectAnsible() *Ansible {
	path, err := exec.LookPath("ansible-inventory")
	if err != nil {
		return &Ansible{}
	}
	return &Ansible{binary: path}
}

// Available reports whether ansible-inventory can be executed.
func (a *Ansible) Available() bool { return a != nil && a.binary != "" }

type ansibleList struct {
	Meta struct {
		HostVars map[string]map[string]any `json:"hostvars"`
	} `json:"_meta"`
}

// HostVars returns the authoritative host vars computed by Ansible.
func (a *Ansible) HostVars(ctx context.Context, file string) (map[string]map[string]any, error) {
	if !a.Available() {
		return nil, fmt.Errorf("ansible-inventory is not available")
	}
	cmd := exec.CommandContext(ctx, a.binary, "-i", file, "--list")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ansible-inventory --list: %w: %s", err, lastLine(stderr.String()))
	}
	var doc ansibleList
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		return nil, fmt.Errorf("parse ansible-inventory output: %w", err)
	}
	if doc.Meta.HostVars == nil {
		return map[string]map[string]any{}, nil
	}
	return doc.Meta.HostVars, nil
}

// Provider computes inventory facts for one Pigsty home. The parsed model is
// cached until the file content changes; nothing is polled in the background.
type Provider struct {
	home    string
	ansible *Ansible

	mu    sync.Mutex
	hash  string
	model *Model
}

// NewProvider creates an inventory provider for a Pigsty home.
func NewProvider(home string) *Provider {
	return &Provider{home: home, ansible: DetectAnsible()}
}

// Engine reports which engine effective value requests will use.
func (p *Provider) Engine() string {
	if p.ansible.Available() {
		return "ansible"
	}
	return "builtin"
}

// Model parses the inventory file, reusing the cached model while it is
// unchanged.
func (p *Provider) Model(file, hash string) (*Model, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.model != nil && p.hash == hash {
		return p.model, nil
	}
	data, err := readFile(file)
	if err != nil {
		return nil, err
	}
	m, err := Load(data)
	if err != nil {
		return nil, err
	}
	p.model, p.hash = m, hash
	return m, nil
}

// CheckFile runs ansible-inventory against a file, used to validate candidate
// configurations. It is a no-op when ansible is unavailable.
func (p *Provider) CheckFile(ctx context.Context, file string) error {
	if !p.ansible.Available() {
		return nil
	}
	_, err := p.ansible.HostVars(ctx, file)
	return err
}

// Effective computes effective values for a host, preferring ansible-inventory
// and falling back to the builtin engine.
func (p *Provider) Effective(ctx context.Context, file, hash, host string, defaults map[string]any,
	sensitive func(string) bool) (string, []Var, error) {
	m, err := p.Model(file, hash)
	if err != nil {
		return p.Engine(), nil, err
	}
	if p.ansible.Available() {
		hostVars, err := p.ansible.HostVars(ctx, file)
		if err == nil {
			if vars, err := m.EffectiveFromHostVars(host, hostVars[host], defaults, sensitive); err == nil {
				return "ansible", vars, nil
			}
		}
	}
	vars, err := m.Effective(host, defaults, sensitive)
	if err != nil {
		return "builtin", nil, err
	}
	return "builtin", vars, nil
}
