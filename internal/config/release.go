package config

import (
	_ "embed"
	"encoding/json"
)

// releaseManifest 为本地运行提供代码版本；镜像构建可以覆盖为发布标签。
//
//go:embed release.json
var releaseManifest []byte

// BuildVersion 由镜像构建注入，避免程序界面始终显示初始版本。
var BuildVersion string

// ReleaseVersion 返回构建版本，未注入时使用代码内的发布清单。
func ReleaseVersion() string {
	if BuildVersion != "" {
		return BuildVersion
	}
	var manifest struct {
		Version string `json:"version"`
	}
	_ = json.Unmarshal(releaseManifest, &manifest)
	return manifest.Version
}
