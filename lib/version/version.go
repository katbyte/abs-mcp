// Package version exposes the abs-mcp version, set at build time or derived from module build info.
package version

import "runtime/debug"

// Version is the release, set at build time with -X. It falls back to the
// module's build info, and to "dev" when there is nothing to fall back to.
var Version = "dev"

// GitCommit is the commit the binary was built from, set at build time with -X.
var GitCommit string

func init() {
	// an -X flag given an empty value leaves this empty rather than "dev", so
	// treat both as unset: a build should never report a blank version
	if Version != "" && Version != "dev" {
		return
	}
	Version = "dev"

	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "(devel)" && info.Main.Version != "" {
		Version = info.Main.Version
	}
}
