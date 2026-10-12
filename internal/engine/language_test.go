package engine

import (
	"media-harbor/internal/domain"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestYouTubeRequestLanguage 覆盖全部系统语言，扫描和下载复用同一请求配置。
func TestYouTubeRequestLanguage(t *testing.T) {
	for _, language := range []string{"zh-CN", "en", "hi", "ru", "ja"} {
		args := (CLI{}).common(domain.Subscription{Platform: "youtube", RequestLanguage: language})
		i := slices.Index(args, "--extractor-args")
		if i < 0 || args[i+1] != "youtube:lang="+language {
			t.Fatal(language, args)
		}
	}
	if slices.Contains((CLI{}).common(domain.Subscription{Platform: "instagram", RequestLanguage: "zh-CN"}), "--extractor-args") {
		t.Fatal("YouTube 语言参数传入 Instagram")
	}
}

// TestPublicationTitle 标题与发布日期来自同一份真实下载元数据。
func TestPublicationTitle(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".metadata.json"), []byte(`{"title":"中文标题","upload_date":"20260531"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if PublicationTitle(dir) != "中文标题" || PublicationDate(dir).Format("20060102") != "20260531" {
		t.Fatal("未读取实际下载清单")
	}
}
