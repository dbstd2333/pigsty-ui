// Package ansible carries the ansible callback plugin the console installs into
// its data directory before running any playbook.
package ansible

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed callback_plugins/pigsty_events.py
var files embed.FS

// PluginName is the name the plugin registers itself under.
const PluginName = "pigsty_events"

// Release writes the callback plugin into <dataDir>/ansible/callback_plugins and
// returns that directory. It is called on every start so the file always
// matches the running console build.
func Release(dataDir string) (string, error) {
	dir := filepath.Join(dataDir, "ansible", "callback_plugins")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create ansible dir: %w", err)
	}
	data, err := files.ReadFile("callback_plugins/pigsty_events.py")
	if err != nil {
		return "", fmt.Errorf("read embedded callback plugin: %w", err)
	}
	path := filepath.Join(dir, PluginName+".py")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("write callback plugin: %w", err)
	}
	return dir, nil
}
