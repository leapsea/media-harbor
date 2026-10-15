package engine

import (
	"context"
	"encoding/json"
	"errors"
	"media-harbor/internal/domain"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// 评论重新解析受限时仍保存新评论，字幕与音频不能被仅图片清单拖入重试。
// 有效的新清单则继续生效，避免一直使用旧资源地址。
func TestYouTubeCommentsKeepUsableDownloadSnapshot(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("未安装 Python")
	}
	for _, degraded := range []bool{true, false} {
		t.Run(map[bool]string{true: "images-only", false: "fresh-formats"}[degraded], func(t *testing.T) {
			root := t.TempDir()
			trace := filepath.Join(root, "trace")
			quoted, _ := json.Marshal(trace)
			script := "#!" + python + "\n" + `import sys,json,pathlib
a=sys.argv[1:];p=pathlib.Path(` + string(quoted) + `)
phase='comments' if '--write-comments' in a else 'parse' if '--dump-single-json' in a else 'metadata' if '--write-info-json' in a else 'subtitle' if '--write-subs' in a else 'audio' if '-x' in a else 'video'
with p.open('a') as f: f.write(phase+'\n')
info={'id':'id','title':'fixture','subtitles':{'en':[{'url':'https://example.test/sub.vtt'}]},
      'formats':[{'format_id':'old','vcodec':'h264','acodec':'aac','url':'https://example.test/old.mp4'}]}
if phase=='parse': print(json.dumps(info))
elif phase=='comments':
 info['comments']=[{'id':'new-comment','text':'fresh comment'}]
 info['formats']=[{'format_id':'sb','vcodec':'none','acodec':'none'}] if ` + map[bool]string{true: "True", false: "False"}[degraded] + ` else [{'format_id':'fresh','vcodec':'h264','acodec':'aac','url':'https://example.test/fresh.mp4'}]
 if ` + map[bool]string{true: "False", false: "True"}[degraded] + `: pathlib.Path(a[a.index('-o')+1]).with_name('fixture-id.info.json').write_text(json.dumps(info))
 print(json.dumps(info))
elif phase=='metadata':
 loaded=json.loads(pathlib.Path(a[a.index('--load-info-json')+1]).read_text())
 assert loaded['comments'][0]['text']=='fresh comment',loaded
 pathlib.Path(a[a.index('-o')+1]).with_name('fixture-id.info.json').write_text(json.dumps(loaded))
elif phase in ('subtitle','audio'):
 loaded=json.loads(pathlib.Path(a[a.index('--load-info-json')+1]).read_text())
 assert loaded['formats'][0]['format_id']==` + map[bool]string{true: "'old'", false: "'fresh'"}[degraded] + `,loaded
`
			if err := os.WriteFile(filepath.Join(root, "yt-dlp"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
			var logs []string
			options := domain.YouTubeOptions{Video: true, Format: "best", Comments: true, Metadata: true, Subtitles: true, SubtitleLanguages: "en", ExtraAudio: true, Fragments: 1}
			err := (CLI{DataDir: root}).Download(context.Background(), domain.Subscription{Platform: "youtube"}, domain.Content{URL: "https://example.test/video"}, options, filepath.Join(root, "work"), func(s string) { logs = append(logs, s) })
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := os.ReadFile(trace)
			want := "parse\nvideo\ncomments\nsubtitle\naudio\n"
			if degraded {
				want = "parse\nvideo\ncomments\nmetadata\nsubtitle\naudio\n"
			}
			if string(raw) != want {
				t.Fatalf("不应重新解析或跳过附属项：%s", raw)
			}
			comments, err := os.ReadFile(filepath.Join(root, "work", "fixture-id.info.json"))
			if err != nil || !strings.Contains(string(comments), "fresh comment") {
				t.Fatalf("新评论未保存：%s %v", comments, err)
			}
			if strings.Contains(strings.Join(logs, "\n"), "沿用主任务的有效清单") != degraded {
				t.Fatal(logs)
			}
		})
	}
}

// 无视频编码的独立音轨仍是媒体；故事板和缺失清单不算音视频格式。
func TestYouTubeHasMediaFormats(t *testing.T) {
	for raw, want := range map[string]bool{
		`{"formats":[{"vcodec":"none","acodec":"opus"}]}`: true,
		`{"formats":[{"vcodec":"av1","acodec":"none"}]}`:  true,
		`{"formats":[{"vcodec":"none","acodec":"none"}]}`: false,
		`{"formats":[]}`: false,
		`{}`:             false,
	} {
		if youtubeHasMediaFormats([]byte(raw)) != want {
			t.Fatal(raw)
		}
	}
}

// 评论超时仅终止该 SDK，后续字幕和音频仍执行；主上下文不能被当作服务停止。
func TestYouTubeCommentTimeoutContinuesAttachments(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("未安装 Python")
	}
	root := t.TempDir()
	trace, _ := json.Marshal(filepath.Join(root, "trace"))
	script := "#!" + python + "\n" + `import sys,json,pathlib,time
a=sys.argv[1:]
phase='comments' if '--write-comments' in a else 'parse' if '--dump-single-json' in a else 'subtitle' if '--write-subs' in a else 'audio' if '-x' in a else 'video'
with open(` + string(trace) + `,'a') as f:f.write(phase+'\n')
if phase=='comments':
 params=a[a.index('--extractor-args')+1]
 assert 'max_comments=42' in params and 'comment_sort=new' in params,params
 time.sleep(30)
if phase=='parse':print(json.dumps({'id':'id','title':'fixture','subtitles':{'en':[{}]}}))
`
	if err := os.WriteFile(filepath.Join(root, "yt-dlp"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx := context.Background()
	started := time.Now()
	err = (CLI{DataDir: root}).Download(ctx, domain.Subscription{Platform: "youtube"}, domain.Content{URL: "https://example.test/video"}, domain.YouTubeOptions{Video: true, Format: "best", Comments: true, CommentLimit: 42, CommentTimeoutSeconds: 1, Subtitles: true, SubtitleLanguages: "en", ExtraAudio: true}, filepath.Join(root, "work"), func(string) {})
	if !errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil || time.Since(started) > 5*time.Second {
		t.Fatal(err, time.Since(started))
	}
	raw, _ := os.ReadFile(filepath.Join(root, "trace"))
	if string(raw) != "parse\nvideo\ncomments\nsubtitle\naudio\n" {
		t.Fatal(string(raw))
	}
}
