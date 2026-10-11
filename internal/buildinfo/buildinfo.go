// Package buildinfo holds the build metadata injected with -ldflags -X (FND-02).
package buildinfo

import "runtime"

// Set at link time, e.g. -X github.com/xiaolai/recite/internal/buildinfo.version=v1.0.0.
// They stay unexported variables so -X can write them and nothing else can.
var (
	version string
	commit  string
	date    string
)

// Info is the build metadata of the running binary. No field is ever empty.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Date      string `json:"date"`
	GoVersion string `json:"go"`
}

// Get returns the injected metadata, with "dev" / "unknown" for values not injected.
func Get() Info {
	return Info{
		Version:   orDefault(version, "dev"),
		Commit:    orDefault(commit, "unknown"),
		Date:      orDefault(date, "unknown"),
		GoVersion: runtime.Version(),
	}
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
