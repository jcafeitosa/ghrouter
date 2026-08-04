package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"runtime/debug"
)

type BuildIdentity struct {
	Version      string `json:"version"`
	BinarySHA256 string `json:"binary_sha256,omitempty"`
	VCSRevision  string `json:"vcs_revision,omitempty"`
	VCSTime      string `json:"vcs_time,omitempty"`
	VCSModified  bool   `json:"vcs_modified,omitempty"`
}

func CurrentBuildIdentity() BuildIdentity {
	identity := BuildIdentity{Version: Version}
	if executable, err := os.Executable(); err == nil {
		if file, err := os.Open(executable); err == nil {
			hash := sha256.New()
			if _, copyErr := io.Copy(hash, file); copyErr == nil {
				identity.BinarySHA256 = hex.EncodeToString(hash.Sum(nil))
			}
			_ = file.Close()
		}
	}
	if buildInfo, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range buildInfo.Settings {
			switch setting.Key {
			case "vcs.revision":
				identity.VCSRevision = setting.Value
			case "vcs.time":
				identity.VCSTime = setting.Value
			case "vcs.modified":
				identity.VCSModified = setting.Value == "true"
			}
		}
	}
	return identity
}
