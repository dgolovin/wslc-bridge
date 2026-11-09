package buildinfo

import "runtime"

var (
	Version = "0.1.1-dev"
	Commit  = "unknown"
	Date    = "unknown"
)

type VersionInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"buildDate"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	GoVersion string `json:"goVersion"`
}

func Info() VersionInfo {
	return VersionInfo{
		Version: Version, Commit: Commit, BuildDate: Date,
		OS: runtime.GOOS, Arch: runtime.GOARCH, GoVersion: runtime.Version(),
	}
}
