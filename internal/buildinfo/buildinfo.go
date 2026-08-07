// Package buildinfo reports reproducible executable build metadata.
package buildinfo

import (
	"runtime"
	"runtime/debug"
	"strings"
)

// These values are overridden by release builds with -ldflags -X. Development
// builds still recover VCS metadata embedded by the Go toolchain.
var (
	Version = "development"
	Commit  = ""
	Date    = ""
)

// Info is the stable version command payload.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	Date      string `json:"date,omitempty"`
	Dirty     bool   `json:"dirty,omitempty"`
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

// Read combines release-injected values with Go's embedded module/VCS data.
func Read() Info {
	info := Info{Version: Version, Commit: Commit, Date: Date, GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH}
	if build, ok := debug.ReadBuildInfo(); ok {
		if info.Version == "development" && build.Main.Version != "" && build.Main.Version != "(devel)" {
			info.Version = build.Main.Version
		}
		for _, setting := range build.Settings {
			switch setting.Key {
			case "vcs.revision":
				if info.Commit == "" {
					info.Commit = setting.Value
				}
			case "vcs.time":
				if info.Date == "" {
					info.Date = setting.Value
				}
			case "vcs.modified":
				info.Dirty = strings.EqualFold(setting.Value, "true")
			}
		}
	}
	return info
}
