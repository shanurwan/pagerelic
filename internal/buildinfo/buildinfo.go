// Package buildinfo reports the identity of the running binary so every
// output can name the exact build that produced it.
package buildinfo

import (
	"runtime"
	"runtime/debug"
)

// Version is set at link time with -X.
var Version = "dev"

// Info identifies a build.
type Info struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Revision  string `json:"revision,omitempty"`
	Modified  bool   `json:"modified,omitempty"`
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

// Get returns the build identity.
func Get() Info {
	info := Info{Name: "pagerelic", Version: Version, GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				info.Revision = s.Value
			case "vcs.modified":
				info.Modified = s.Value == "true"
			}
		}
	}
	return info
}
