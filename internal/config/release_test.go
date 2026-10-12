package config

import (
	"encoding/json"
	"testing"
)

// 构建版本用于验证镜像标签和应用显示保持一致。
func TestReleaseVersion(t *testing.T) {
	var manifest struct {
		Version string `json:"version"`
	}
	_ = json.Unmarshal(releaseManifest, &manifest)
	if ReleaseVersion() != manifest.Version {
		t.Fatalf("unexpected initial version: %s", ReleaseVersion())
	}
	BuildVersion = "1.0.100"
	t.Cleanup(func() { BuildVersion = "" })
	if ReleaseVersion() != "1.0.100" {
		t.Fatal("build version did not override manifest")
	}
}
