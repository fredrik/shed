// Package version reports the build version of the shed binaries.
//
// Releases are git tags (v0.1.0, ...). `make build` injects `git describe`
// output via -ldflags -X so a dev build reads like v0.1.0-3-g19ad079-dirty.
// A plain `go build` of a tagged checkout still gets its version from the
// module build info Go embeds; anything else reports "dev".
package version

import (
	"fmt"
	"regexp"
	"runtime"
	"runtime/debug"
)

// Set at link time by the Makefile; see LDFLAGS there.
var (
	Version string
	Commit  string
)

// Info describes one binary's build.
type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
	Go      string `json:"go"`
}

// Current returns the running binary's build info.
func Current() Info {
	bi, _ := debug.ReadBuildInfo()
	return resolve(Version, Commit, bi)
}

// String is the one-line form: "v0.1.0 (19ad079, go1.25.6)".
func (i Info) String() string {
	if i.Commit == "" {
		return fmt.Sprintf("%s (%s)", i.Version, i.Go)
	}
	return fmt.Sprintf("%s (%s, %s)", i.Version, i.Commit, i.Go)
}

// resolve merges linker-provided values with Go's embedded build info,
// preferring the former. Split out from Current so tests can drive it.
func resolve(linkVersion, linkCommit string, bi *debug.BuildInfo) Info {
	info := Info{Version: linkVersion, Commit: linkCommit, Go: runtime.Version()}
	if bi != nil {
		if info.Version == "" && isTagVersion(bi.Main.Version) {
			info.Version = bi.Main.Version
		}
		if info.Commit == "" {
			info.Commit = vcsCommit(bi.Settings)
		}
	}
	if info.Version == "" {
		info.Version = "dev"
	}
	return info
}

// pseudoVersion matches the timestamp-hash tail Go synthesises for untagged
// commits (v0.0.0-20260918071511-2469bc317624). That is not a release, and
// the commit is already reported separately, so it counts as "dev".
var pseudoVersion = regexp.MustCompile(`\d{14}-[0-9a-f]{12}`)

func isTagVersion(v string) bool {
	return v != "" && v != "(devel)" && !pseudoVersion.MatchString(v)
}

func vcsCommit(settings []debug.BuildSetting) string {
	var rev, modified string
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	if rev == "" {
		return ""
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if modified == "true" {
		rev += "-dirty"
	}
	return rev
}
