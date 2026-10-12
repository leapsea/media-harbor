package engine

import (
	"context"
	"encoding/json"
	"media-harbor/internal/domain"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestCLIArtifactReuse 记录真实命令参数，验证已有音频不重下、元数据独立刷新和字幕按语言补齐。
func TestCLIArtifactReuse(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("未安装 Python")
	}
	root := t.TempDir()
	trace := filepath.Join(root, "trace.jsonl")
	quotedTrace, _ := json.Marshal(trace)
	script := "#!" + python + "\n" + `import json,sys,pathlib
args=sys.argv[1:]
with open(` + string(quotedTrace) + `, 'a') as f: f.write(json.dumps(args)+'\n')
out=pathlib.Path(args[args.index('-o')+1]).parent if '-o' in args else None
if '--dump-single-json' in args:
    data={'id':'id','title':'new-title','upload_date':'20260102','comments':[{'text':'new'},{'text':'updated'}],'subtitles':{'zh-Hans':[{}],'en':[{}]}}
    if '--write-auto-subs' in args: data['automatic_captions']={'zh-Hant':[{}]}
    if '--write-info-json' in args: (out/'new-title-id.info.json').write_text(json.dumps(data))
    print(json.dumps(data))
else:
    if '--skip-download' not in args: (out/'new-title-id.mp4').write_text('video')
    if '-x' in args: (out/'new-title-id.audio.m4a').write_text('audio')
    if '--write-subs' in args: (out/'new-title-id.en.vtt').write_text('subtitle')
    if '--write-auto-subs' in args: (out/'new-title-id.zh-Hant.vtt').write_text('automatic subtitle')
`
	if err = os.WriteFile(filepath.Join(root, "yt-dlp"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	audio := filepath.Join(root, "old-title-id.audio.m4a")
	subtitle := filepath.Join(root, "old-title-id.zh-Hans.vtt")
	for _, p := range []string{audio, subtitle} {
		if err = os.WriteFile(p, []byte("existing"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := os.Stat(audio)
	options := domain.YouTubeOptions{Video: true, Format: "bestvideo+bestaudio/best", ExtraAudio: true, AudioFormat: "m4a", AudioQuality: "0", Metadata: true, Comments: true, Subtitles: true, AutoSubtitles: true, SubtitleLanguages: "zh.*,en", Fragments: 6}
	work := filepath.Join(root, "work")
	item := domain.Content{URL: "https://example.com/video", Paths: []string{audio, subtitle}}
	if err = (CLI{DataDir: root}).Download(context.Background(), domain.Subscription{Platform: "youtube"}, item, options, work, func(string) {}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(trace)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 3 {
		t.Fatalf("unexpected commands=%s", raw)
	}
	for i, line := range lines {
		var args []string
		_ = json.Unmarshal([]byte(line), &args)
		if slices.Contains(args, "-x") {
			t.Fatal("已下载音频被重新下载", args)
		}
		if i == 0 && (!slices.Contains(args, "--skip-download") || slices.Contains(args, "--write-comments")) {
			t.Fatal(args)
		}
		if i == 0 {
			n := slices.Index(args, "--sub-langs")
			if !slices.Contains(args, "--write-auto-subs") || n < 0 || args[n+1] != "all,-.*" {
				t.Fatal("元数据未提取自动翻译清单或会提前下载字幕", args)
			}
		}
		if i == 1 {
			if slices.Contains(args, "--force-overwrites") || slices.Contains(args, "--write-info-json") {
				t.Fatal("媒体执行包含元数据覆盖", args)
			}
			if !slices.Contains(args, "--load-info-json") || slices.Contains(args, "--write-subs") || slices.Contains(args, "--write-auto-subs") {
				t.Fatal("主视频未独立执行", args)
			}
		}
		if i == 2 && (!slices.Contains(args, "--write-comments") || !slices.Contains(args, "--force-overwrites")) {
			t.Fatal("评论未作为主视频之后的附属任务", args)
		}
	}
	after, _ := os.Stat(audio)
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("已有音频被改写")
	}
	metadata, _ := os.ReadFile(filepath.Join(work, "new-title-id.info.json"))
	if !strings.Contains(string(metadata), "updated") {
		t.Fatal(string(metadata))
	}
}

// TestNeedsSupplement 区分已下载媒体与可变元数据，未完成文件不能满足需求。
func TestNeedsSupplement(t *testing.T) {
	root := t.TempDir()
	audio := filepath.Join(root, "id.audio.m4a")
	video := filepath.Join(root, "id.mp4")
	_ = os.WriteFile(audio, []byte("audio"), 0644)
	_ = os.WriteFile(video, []byte("video"), 0644)
	item := domain.Content{Paths: []string{audio, video}}
	options := domain.YouTubeOptions{Video: true, ExtraAudio: true, AudioFormat: "m4a"}
	if NeedsSupplement("youtube", item, options) {
		t.Fatal("完整媒体被重新排队")
	}
	options.Metadata = true
	if !NeedsSupplement("youtube", item, options) {
		t.Fatal("元数据未刷新")
	}
	options.Metadata = false
	_ = os.Remove(audio)
	if !NeedsSupplement("youtube", item, options) {
		t.Fatal("缺失音频未补全")
	}
	for _, name := range []string{"id.mp4.part", "id.f137.mp4", "id.temp.mp4", ".metadata.json"} {
		p := filepath.Join(root, name)
		_ = os.WriteFile(p, []byte("partial"), 0644)
		if CompletedArtifact(p) {
			t.Fatal("中间文件被当成完成产物", p)
		}
	}
}
