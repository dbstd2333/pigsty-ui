// Package version exposes the Pigsty Console build version.
package version

// Version is the console release version; it can be overridden at build time
// with -ldflags "-X .../internal/version.Version=...".
var Version = "0.1.0"

// Commit is an optional build commit hash appended to Version.
var Commit = ""

// String returns the human readable version string, e.g. "0.1.0+ab12cd3".
func String() string {
	if Commit == "" {
		return Version
	}
	return Version + "+" + Commit
}
