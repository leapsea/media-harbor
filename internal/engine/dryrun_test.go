package engine

import (
	"context"
	"io/fs"
	"media-harbor/internal/domain"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// snapshotDryFiles 比较文件内容和修改时间，检测 Cookie 回写及任何新增文件。
func snapshotDryFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			info, _ := entry.Info()
			result[path] = string(raw) + info.ModTime().String()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// TestInstagramDryRunReadOnly 使用真实 gallery-dl 与本地 HTTP 响应验证媒体、头像和只读缓存。
func TestInstagramDryRunReadOnly(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil || exec.Command(python, "-c", "import gallery_dl").Run() != nil {
		t.Skip("未安装 gallery-dl")
	}
	var fail atomic.Bool
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if fail.Load() {
			w.WriteHeader(429)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "new", Value: "server-value"})
		_, _ = w.Write([]byte("image-fixture"))
	}))
	defer server.Close()
	root := t.TempDir()
	fixture := "#!" + python + "\n" + `import sys,re
from gallery_dl import extractor,cache
from gallery_dl.extractor.common import Extractor,Message
class Fixture(Extractor):
    category='instagram'
    subcategory='fixture'
    @classmethod
    def from_url(cls,address): return cls(re.match('.*',address))
    def items(self):
        assert cache.path()==':memory:'
        assert cache.database() is not None
        yield Message.Directory, None, {'username':'author'}
        yield Message.Url, ` + strconv.Quote(server.URL) + `, {'post_id':'one','username':'author','extension':'jpg'}
extractor.find=lambda address:Fixture.from_url(address)
exec(sys.argv[sys.argv.index('-c')+1])
`
	if err = os.WriteFile(filepath.Join(root, "python3"), []byte(fixture), 0755); err != nil {
		t.Fatal(err)
	}
	cli := CLI{DataDir: root}
	if err = os.MkdirAll(filepath.Join(root, "cookies"), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(cli.CookiePath("instagram"), []byte("# Netscape HTTP Cookie File\n.example.test\tTRUE\t/\tFALSE\t0\told\tvalue\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	before := snapshotDryFiles(t, root)
	for _, failure := range []bool{false, true} {
		fail.Store(failure)
		err = cli.DryRun(context.Background(), domain.Subscription{Platform: "instagram", URL: "https://www.instagram.com/author/"}, domain.YouTubeOptions{}, 10, func(line string) { t.Log(line) })
		if (err != nil) != failure {
			t.Fatal("请求结果不正确", failure, err)
		}
		if !reflect.DeepEqual(before, snapshotDryFiles(t, root)) {
			t.Fatal("Instagram dryrun 写入了文件")
		}
	}
	if requests.Load() < 3 {
		t.Fatal("未验证媒体、头像与失败响应", requests.Load())
	}
}

// TestDryRunReadOnly 用真实 yt-dlp 的 Cookie 生命周期和离线响应替身验证不落盘。
func TestDryRunReadOnly(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil || exec.Command(python, "-c", "import yt_dlp").Run() != nil {
		t.Skip("未安装 yt-dlp")
	}
	root := t.TempDir()
	fixture := "#!" + python + "\n" + `import sys,io,json,yt_dlp
def extract(self,address,download=False):
    assert download is False
    assert self.params['simulate'] and self.params['cachedir'] is False
    self.cookiejar.set_cookie(__import__('http.cookiejar',fromlist=['Cookie']).Cookie(0,'new','value',None,False,'.example.test',True,True,'/',True,False,None,True,None,None,{}))
    return {'id':'dry-media','subtitles':{'zh-CN':[{'url':'https://example.test/sub?lang=zh-CN','ext':'vtt'}]},'automatic_captions':{'zh-Hans-en':[{'url':'https://example.test/sub?tlang=zh-Hans','ext':'vtt'}]}}
class Response(io.BytesIO):
    status=200
def request(self,req):
    assert 'tlang' not in req.url
    return Response(b'WEBVTT\n\n00:00.000 --> 00:01.000\nfixture\n')
yt_dlp.YoutubeDL.extract_info=extract
yt_dlp.YoutubeDL.urlopen=request
exec(sys.argv[sys.argv.index('-c')+1])
`
	if err = os.WriteFile(filepath.Join(root, "python3"), []byte(fixture), 0755); err != nil {
		t.Fatal(err)
	}
	cli := CLI{DataDir: root}
	if err = os.MkdirAll(filepath.Join(root, "cookies"), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(cli.CookiePath("youtube"), []byte("# Netscape HTTP Cookie File\n.example.test\tTRUE\t/\tFALSE\t0\told\tvalue\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	before := snapshotDryFiles(t, root)
	var logs []string
	options := domain.YouTubeOptions{Subtitles: true, AutoSubtitles: true, SubtitleLanguages: "zh.*"}
	err = cli.DryRun(context.Background(), domain.Subscription{Platform: "youtube", URL: "https://example.test/video"}, options, 10, func(line string) { logs = append(logs, line) })
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, snapshotDryFiles(t, root)) {
		t.Fatal("dryrun 改变了文件或 Cookie")
	}
	if text := strings.Join(logs, "\n"); !strings.Contains(text, "HTTP 200") || !strings.Contains(text, "zh-CN") {
		t.Fatal(text)
	}
}

// TestDryRunCancellation 验证关闭请求会结束引擎，不留下后台验证进程。
func TestDryRunCancellation(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "python3"), []byte("#!/bin/sh\nexec sleep 60\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := (CLI{DataDir: root}).DryRun(ctx, domain.Subscription{Platform: "youtube"}, domain.YouTubeOptions{}, 10, func(string) {})
	if err == nil || time.Since(started) > 2*time.Second {
		t.Fatal(err, time.Since(started))
	}
}
