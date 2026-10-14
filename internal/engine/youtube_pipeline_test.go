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

// TestYouTubeChildren 用实际子进程记录父子执行顺序，附属失败不会阻止其余附属项。
func TestYouTubeChildren(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("未安装 Python")
	}
	for _, failMain := range []bool{false, true} {
		t.Run(map[bool]string{false: "child-failure", true: "main-failure"}[failMain], func(t *testing.T) {
			root := t.TempDir()
			trace := filepath.Join(root, "stages")
			quoted, _ := json.Marshal(trace)
			script := "#!" + python + "\n" + `import sys,json,pathlib
a=sys.argv[1:]
phase='parse' if '--dump-single-json' in a else 'video' if '--skip-download' not in a and '-x' not in a else 'audio' if '-x' in a else 'subtitle' if '--write-subs' in a else 'thumbnail'
if '--write-comments' in a: phase='comments'
with open(` + string(quoted) + `, 'a') as f: f.write(phase+'\n')
if phase=='comments' or (phase=='video' and ` + map[bool]string{false: "False", true: "True"}[failMain] + `):
 print('ERROR: fixture stage failure',file=sys.stderr);sys.exit(1)
if phase=='parse': print(json.dumps({'id':'id','title':'fixture','subtitles':{'en':[{}],'zh-CN':[{}]}}))
`
			if err := os.WriteFile(filepath.Join(root, "yt-dlp"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
			o := domain.YouTubeOptions{Video: true, Format: "best", Comments: true, Subtitles: true, SubtitleLanguages: "zh.*,en", Thumbnail: true, ExtraAudio: true, AudioFormat: "m4a", AudioQuality: "0", Fragments: 1}
			var logs []string
			err := (CLI{DataDir: root}).Download(context.Background(), domain.Subscription{Platform: "youtube"}, domain.Content{URL: "https://example.test/video"}, o, filepath.Join(root, "work"), func(s string) { logs = append(logs, s) })
			if err == nil {
				t.Fatal("丢失阶段失败")
			}
			raw, _ := os.ReadFile(trace)
			want := "parse\nvideo\ncomments\nsubtitle\nthumbnail\naudio\n"
			if failMain {
				want = "parse\nvideo\n"
			}
			if string(raw) != want {
				t.Fatalf("stages=%s want=%s", raw, want)
			}
			if !failMain && !strings.Contains(strings.Join(logs, "\n"), "附属任务全部结束；失败 1 项") {
				t.Fatal(logs)
			}
		})
	}
}

// TestYouTube403Refresh 验证媒体拒绝后只重新解析一次，成功前不进入附属阶段。
func TestYouTube403Refresh(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("未安装 Python")
	}
	root := t.TempDir()
	trace := filepath.Join(root, "trace")
	quoted, _ := json.Marshal(trace)
	script := "#!" + python + "\n" + `import sys,json,pathlib
p=pathlib.Path(` + string(quoted) + `)
a=sys.argv[1:]
parse='--dump-single-json' in a
old=p.read_text() if p.exists() else ''
with p.open('a') as f: f.write('parse\n' if parse else 'video\n')
if parse: print(json.dumps({'id':'id','title':'fixture'}))
elif 'video' not in old:
 print('ERROR: HTTP Error 403: Forbidden',file=sys.stderr);sys.exit(1)
`
	if err := os.WriteFile(filepath.Join(root, "yt-dlp"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	err = (CLI{DataDir: root}).Download(context.Background(), domain.Subscription{Platform: "youtube"}, domain.Content{URL: "https://example.test/video"}, domain.YouTubeOptions{Video: true, Format: "best", Fragments: 1}, filepath.Join(root, "work"), func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(trace)
	if string(raw) != "parse\nvideo\nparse\nvideo\n" {
		t.Fatal(string(raw))
	}
}

// TestYouTubeUnavailableFormatRefresh 验证空清单刷新一次、保留画质上限，持续失败不启动附属任务。
func TestYouTubeUnavailableFormatRefresh(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("未安装 Python")
	}
	for _, permanent := range []bool{false, true} {
		t.Run(map[bool]string{false: "refresh-success", true: "still-unavailable"}[permanent], func(t *testing.T) {
			root := t.TempDir()
			trace := filepath.Join(root, "trace")
			quoted, _ := json.Marshal(trace)
			script := "#!" + python + "\n" + `import sys,json,pathlib
p=pathlib.Path(` + string(quoted) + `)
a=sys.argv[1:];parse='--dump-single-json' in a
old=p.read_text() if p.exists() else ''
with p.open('a') as f: f.write('parse\n' if parse else 'video\n')
assert a[a.index('-f')+1]=='bestvideo[height<=1080]+bestaudio/best[height<=1080]'
assert a[a.index('--format-sort')+1]=='hasvid,res,fps' and '--format-sort-force' in a
if parse: print(json.dumps({'id':'id','title':'fixture'}))
elif 'video' not in old or ` + map[bool]string{false: "False", true: "True"}[permanent] + `:
 print('ERROR: Requested format is not available',file=sys.stderr);sys.exit(1)
`
			if err := os.WriteFile(filepath.Join(root, "yt-dlp"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
			options := domain.YouTubeOptions{Video: true, Format: "bestvideo[height<=1080]+bestaudio/best[height<=1080]", Fragments: 1}
			var logs []string
			err := (CLI{DataDir: root}).Download(context.Background(), domain.Subscription{Platform: "youtube"}, domain.Content{URL: "https://example.test/video"}, options, filepath.Join(root, "work"), func(s string) { logs = append(logs, s) })
			if permanent && (err == nil || !strings.Contains(err.Error(), "重新解析后仍无")) {
				t.Fatalf("持续失败应说明无可下载格式：%v", err)
			}
			if !permanent && err != nil {
				t.Fatal(err)
			}
			raw, _ := os.ReadFile(trace)
			if string(raw) != "parse\nvideo\nparse\nvideo\n" {
				t.Fatalf("清单应只刷新一次：%s", raw)
			}
			if !strings.Contains(strings.Join(logs, "\n"), "保持原画质设置") {
				t.Fatal(logs)
			}
		})
	}
}

// TestYouTubeFormatDiagnostics 排除故事板和音频，并保留未知尺寸及实际选流信息。
func TestYouTubeFormatDiagnostics(t *testing.T) {
	var logs []string
	logYouTubeFormats([]byte(`{"formats":[{"vcodec":"none","width":420,"height":180},{"vcodec":"h264","width":1920,"height":1080},{"vcodec":"av1","width":3840,"height":2160}],"format_id":"401+251","width":3840,"height":2160}`), "video", func(s string) { logs = append(logs, s) })
	if len(logs) != 1 || !strings.Contains(logs[0], "可用视频流 2；最高尺寸 3840x2160；选中格式 401+251；选中尺寸 3840x2160") {
		t.Fatal(logs)
	}
}
