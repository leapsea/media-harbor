package engine

import (
	"context"
	"encoding/json"
	"media-harbor/internal/domain"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestAutoSubtitleMetadataIntegration 使用真实 yt-dlp 的参数解析和字幕写入流程。
// 仅替换平台提取器，离线复现：未启用自动字幕时不返回翻译语言。
func TestAutoSubtitleMetadataIntegration(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil || exec.Command(python, "-c", "import yt_dlp").Run() != nil {
		t.Skip("未安装 Python yt-dlp")
	}
	root := t.TempDir()
	script := "#!" + python + "\n" + `import yt_dlp
from yt_dlp.extractor.common import InfoExtractor
class FixtureIE(InfoExtractor):
    _VALID_URL = r'https://fixture.example/(?P<id>video)'
    def _real_extract(self, url):
        auto = {'zh-Hans': [{'ext': 'vtt', 'url': 'https://fixture.example/captions?lang=en&tlang=zh-Hans', 'data': 'WEBVTT\n\n00:00.000 --> 00:01.000\ntranslation\n'}]} if self.get_param('writeautomaticsub') else {}
        return {'id': 'video', 'title': 'fixture', 'formats': [{'url': 'https://fixture.example/video.mp4', 'ext': 'mp4'}], 'automatic_captions': auto}
def add_extractors(self):
    self.add_info_extractor(FixtureIE())
yt_dlp.YoutubeDL.add_default_info_extractors = add_extractors
yt_dlp.main()
`
	if err = os.WriteFile(filepath.Join(root, "yt-dlp"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	work := filepath.Join(root, "work")
	options := domain.YouTubeOptions{AutoSubtitles: true, SubtitleLanguages: "zh.*", Metadata: true, Fragments: 1}
	if err = (CLI{DataDir: root}).Download(context.Background(), domain.Subscription{Platform: "youtube"}, domain.Content{URL: "https://fixture.example/video"}, options, work, func(line string) { t.Log(line) }); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(work, "fixture-video.zh-Hans.vtt")); err != nil || len(raw) == 0 {
		t.Fatal("自动翻译字幕未下载", err)
	}
	raw, err := os.ReadFile(filepath.Join(work, ".metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	var metadata struct {
		Automatic map[string]json.RawMessage `json:"automatic_captions"`
		Requested map[string]json.RawMessage `json:"requested_subtitles"`
	}
	if err = json.Unmarshal(raw, &metadata); err != nil || len(metadata.Automatic) != 1 || len(metadata.Requested) != 0 {
		t.Fatal("元数据清单不完整或元数据阶段提前下载字幕", metadata, err)
	}
}

// TestChineseSubtitlePreference 覆盖人工/自动原始中文、翻译回退、语言排除与已有文件复用。
func TestChineseSubtitlePreference(t *testing.T) {
	metadata := []byte(`{"subtitles":{"zh-CN":[{"url":"https://example.test/captions?lang=zh-CN"}],"en":[{}]},"automatic_captions":{"zh-CN":[{"url":"https://example.test/captions?lang=zh-CN"}],"zh-Hans-zh-CN":[{"url":"https://example.test/captions?tlang=zh-Hans"}],"zh-Hant-zh-CN":[{"url":"https://example.test/captions?tlang=zh-Hant"}]}}`)
	options := domain.YouTubeOptions{Subtitles: true, AutoSubtitles: true, SubtitleLanguages: "zh.*,en"}
	if got := (ArtifactInventory{}).missingSubtitles(options, metadata); got != "zh-CN" {
		t.Fatal("原始中文未排除翻译版本", got)
	}
	options.Subtitles = false
	if got := (ArtifactInventory{}).missingSubtitles(options, metadata); got != "zh-CN" {
		t.Fatal("自动原始中文未优先", got)
	}
	root := t.TempDir()
	path := filepath.Join(root, "video.zh-CN.vtt")
	if err := os.WriteFile(path, []byte("WEBVTT"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := (ArtifactInventory{Paths: []string{path}}).missingSubtitles(options, metadata); got != "" {
		t.Fatal("已有原始中文仍请求翻译字幕", got)
	}
	fallback := []byte(`{"automatic_captions":{"zh-Hans":[{"url":"https://example.test/captions?lang=en&tlang=zh-Hans"}],"zh-Hant":[{"url":"https://example.test/captions?lang=en&tlang=zh-Hant"}]}}`)
	if got := (ArtifactInventory{}).missingSubtitles(options, fallback); got != "zh-Hans" {
		t.Fatal("没有原始中文时未保留翻译回退", got)
	}
	options.SubtitleLanguages = "zh.*,-zh-Hant"
	if got := (ArtifactInventory{}).missingSubtitles(options, fallback); got != "zh-Hans" {
		t.Fatal("回退未遵循用户语言排除", got)
	}
	options.SubtitleLanguages = "en"
	options.Subtitles = true
	if got := (ArtifactInventory{}).missingSubtitles(options, metadata); strings.Contains(got, "zh") || got != "en" {
		t.Fatal("未选择中文时改变了字幕语言", got)
	}
	options.SubtitleLanguages = "en,zh.*"
	if got := (ArtifactInventory{}).missingSubtitles(options, metadata); got != "en" {
		t.Fatal("未遵循配置语言顺序", got)
	}
}
