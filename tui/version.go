package tui

import "runtime/debug"

// version is the RCSS build version. It defaults to "dev" and can be set at
// build time with -ldflags "-X github.com/dougmb/rcss/tui.version=v1.2.3".
var version = "dev"

// Version returns the build version, for `rcss version`.
func Version() string { return appVersion() }

// appVersion returns the build version, falling back to the module version
// embedded by `go install module@version` when no ldflags value was set.
func appVersion() string {
	if version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return version
}
