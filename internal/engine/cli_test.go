package engine

import (
	"context"
	"errors"
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestCLIIntegration 运行已安装的真实引擎，素材由本地 FFmpeg 生成。
// 不访问 Instagram 或 YouTube，只验证下载集成和音频后处理。
func TestCLIIntegration(t *testing.T) {
	// 日常核心测试不要求安装引擎或监听本机端口。
	if os.Getenv("RUN_ENGINE_INTEGRATION") != "1" {
		t.Skip("设置 RUN_ENGINE_INTEGRATION=1 运行真实引擎测试")
	}
	// 显式运行集成测试时，缺失工具应使测试失败。
	for _, name := range []string{"yt-dlp", "gallery-dl", "ffmpeg"} {
		if _, e := exec.LookPath(name); e != nil {
			t.Fatal(e)
		}
	}
	// 非 MP4 素材同时包含视频与音轨，验证 MP4 重封装和两条音频路径。
	root := t.TempDir()
	fixture := filepath.Join(root, "fixture.mkv")
	if out, e := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", "color=c=blue:s=160x120:d=1", "-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-shortest", "-c:v", "mpeg4", "-c:a", "aac", fixture).CombinedOutput(); e != nil {
		t.Fatalf("素材生成: %v %s", e, out)
	}
	// 素材只通过测试服务器提供，不访问真实平台。
	server := httptest.NewServer(http.FileServer(http.Dir(root)))
	defer server.Close()
	// 扫描与下载通过应用使用的 CLI 适配器执行。
	ctx := context.Background()
	cli := CLI{DataDir: root}
	log := func(s string) { t.Log(s) }
	// 两个工具的忽略配置参数不同，实际版本必须都能接受。
	for _, p := range []struct{ platform, name string }{{"instagram", "gallery-dl"}, {"youtube", "yt-dlp"}} {
		args := cli.common(domain.Subscription{Platform: p.platform})
		args = append(args, "--version")
		if _, e := run(ctx, p.name, args, log, true); e != nil {
			t.Fatal(e)
		}
	}
	// generic 提取器也遵循相同 JSON 与下载命令协议。
	sub := domain.Subscription{Platform: "youtube", URL: server.URL + "/fixture.mkv"}
	result, e := collectScan(cli, ctx, sub, 0, log)
	if e != nil || len(result.Items) != 1 {
		t.Fatalf("scan=%+v error=%v", result, e)
	}
	// 主视频之外同时请求独立音频和本地提取。
	options := config.Defaults().YouTube
	options.ExtraAudio = true
	options.ExtractAudio = true
	options.Metadata = true
	// 工作目录单独管理，生成结果不与素材混合。
	work := filepath.Join(root, "work")
	if e = cli.Download(ctx, sub, result.Items[0], options, work, log); e != nil {
		t.Fatal(e)
	}
	// 用磁盘上的实际产物验证后处理结果。
	entries, e := os.ReadDir(work)
	if e != nil {
		t.Fatal(e)
	}
	found := map[string]bool{}
	for _, f := range entries {
		// 四种产物缺少任意一种都说明功能未完成。
		for _, suffix := range []string{".mp4", ".audio.m4a", ".extracted.m4a", ".info.json"} {
			if strings.HasSuffix(f.Name(), suffix) {
				found[suffix] = true
			}
		}
	}
	for _, suffix := range []string{".mp4", ".audio.m4a", ".extracted.m4a", ".info.json"} {
		if !found[suffix] {
			t.Fatalf("缺少 %s: %v", suffix, entries)
		}
	}
	// 再次执行复用真实媒体文件，只刷新 info JSON。
	before := map[string]time.Time{}
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		before[entry.Name()] = info.ModTime()
	}
	if err := cli.Download(ctx, sub, result.Items[0], options, work, log); err != nil {
		t.Fatal(err)
	}
	metadataUpdated := false
	for name, modified := range before {
		info, err := os.Stat(filepath.Join(work, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(name, ".info.json") {
			metadataUpdated = info.ModTime().After(modified)
		} else if !info.ModTime().Equal(modified) {
			t.Fatalf("已有媒体文件被改写：%s", name)
		}
	}
	if !metadataUpdated {
		t.Fatal("再次执行未刷新元数据")
	}

}

// TestScanFormats 使用上游 JSON 协议验证频道、视频和轮播解析。
func TestScanFormats(t *testing.T) {
	// 播放列表身份必须保持为列表 ID，不能误用频道 ID。
	y, e := parseYouTube([]byte(`{"id":"PL123","title":"playlist","channel":"Example","entries":[{"id":"abc","title":"video","url":"abc","upload_date":"20260102"}]}`))
	if e != nil {
		t.Fatal(e)
	}
	if y.StableID != "PL123" || len(y.Items) != 1 || y.Items[0].URL != "https://www.youtube.com/watch?v=abc" || y.Items[0].Published.Format("2006-01-02") != "2026-01-02" {
		t.Fatalf("%+v", y)
	}
	// 单视频订阅的身份与视频下载去重 ID 一致。
	single, e := parseYouTube([]byte(`{"id":"abc","title":"video","channel_id":"UC123","webpage_url":"https://youtube.com/watch?v=abc"}`))
	if e != nil || single.StableID != "abc" {
		t.Fatalf("%+v %v", single, e)
	}
	// 长整数 ID 不能先转 float64，避免精度损失。
	g, e := parseInstagram([]byte(`[[2,{"username":"alice"}],[3,"https://cdn/file.jpg",{"owner_id":123456789012345678,"username":"alice","post_id":"900","num":2,"post_url":"https://instagram.com/p/xyz/","date":"2026-01-02 03:04:05"}]]`))
	if e != nil {
		t.Fatal(e)
	}
	if g.StableID != "123456789012345678" || len(g.Items) != 1 || g.Items[0].RemoteID != "900-2" || g.Items[0].Published.IsZero() {
		t.Fatalf("%+v", g)
	}
}

// TestInstagramResolveScan 复现主页只返回 Queue 消息的命令差异。
func TestInstagramResolveScan(t *testing.T) {
	dir := t.TempDir()
	// 替身遵循 gallery-dl 协议，只有递归扫描参数才返回媒体。
	script := `#!/bin/sh
case "$*" in
 *--resolve-json*) printf '%s' '[3,"https://cdn.example/media.jpg",{"owner_id":"305214053","username":"yen_210","post_id":"900","num":1,"post_url":"https://www.instagram.com/p/example/"}]' ;;
 *) printf '%s' '[6,"https://www.instagram.com/yen_210/posts/",{}]' ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "python3"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	result, err := collectScan(CLI{DataDir: dir}, context.Background(), domain.Subscription{Platform: "instagram", URL: "https://www.instagram.com/yen_210/"}, 0, func(string) {})
	if err != nil || len(result.Items) != 1 || result.StableID != "305214053" {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

// TestInstagramJSONErrors 验证进程退出成功仍可能携带提取错误。
// 解析错误不丢弃此前获得的媒体，真正空内容仍允许正常完成。
func TestInstagramJSONErrors(t *testing.T) {
	for _, raw := range []string{
		`[[-1,{"error":"AuthRequired","message":"Login required"}]]`,
		`[[6,"https://www.instagram.com/yen_210/posts/",{}]`,
	} {
		if _, err := parseInstagram([]byte(raw)); err == nil {
			t.Fatalf("未报告提取失败: %s", raw)
		}
	}
	raw := `[[3,"https://cdn.example/a.jpg",{"post_id":"900","num":1}],[ -1,{"error":"HttpError","message":"分页请求失败"}]]`
	result, err := parseInstagram([]byte(raw))
	if err == nil || !strings.Contains(err.Error(), "分页请求失败") || len(result.Items) != 1 {
		t.Fatalf("部分结果=%+v error=%v", result, err)
	}
	if _, err := parseInstagram([]byte(`[]`)); err != nil {
		t.Fatal("真正的空结果不应判为失败", err)
	}
}

// TestYouTubeOptions 验证本地提取与独立音频选项不会混入主视频命令。
func TestYouTubeOptions(t *testing.T) {
	// 组合字幕、评论和音频功能，检查主命令仍下载视频。
	o := config.Defaults().YouTube
	o.Comments = true
	o.Subtitles = true
	o.AutoSubtitles = true
	o.ExtraAudio = true
	o.ExtractAudio = true
	args := youtubeArgs(o, "/downloads/work")
	for _, flag := range []string{"--write-comments", "--write-info-json", "--write-subs", "--write-auto-subs", "--merge-output-format", "--remux-video"} {
		if !slices.Contains(args, flag) {
			t.Fatal("缺少", flag)
		}
	}
	// 两个封装参数都指定 MP4，画质表达式不增加编码或分辨率限制。
	for i, arg := range args {
		if arg == "--merge-output-format" || arg == "--remux-video" {
			if args[i+1] != "mp4" {
				t.Fatal("视频封装未指定 MP4", args)
			}
		}
	}
	if !slices.Contains(args, "bestvideo+bestaudio/best") {
		t.Fatal("未保持最高画质选择", args)
	}
	// 远程音频另行执行，不允许改变主视频输出语义。
	if slices.Contains(args, "-x") {
		t.Fatal("主视频命令误变为音频下载")
	}
	// 关闭视频且无需本地提取时，只处理附加文件。
	o.Video = false
	o.ExtractAudio = false
	if !slices.Contains(youtubeArgs(o, "work"), "--skip-download") {
		t.Fatal("仅附加内容不应下载视频")
	}
}

// TestScanItemLimit 验证限制传递给引擎分页，避免完整枚举后的截断。
func TestScanItemLimit(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"python3", "yt-dlp"} {
		script := `#!/bin/sh
case "$0" in
 *python3) case " $* " in *" --range 1-10 "*) printf '';; *) exit 9;; esac;;
 *yt-dlp) case " $* " in *" --playlist-end 10 "*) printf '{"id":"PL1","entries":[]}';; *) exit 9;; esac;;
esac
`
		if err := os.WriteFile(filepath.Join(root, name), []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, platform := range []string{"instagram", "youtube"} {
		_, err := collectScan(CLI{DataDir: root}, context.Background(), domain.Subscription{Platform: platform, URL: "https://example.com/"}, 10, func(string) {})
		if err != nil {
			t.Fatalf("%s: %v", platform, err)
		}
	}
}

// TestInstagramDownloadAuthError 保留退出错误链，并显示可操作的登录原因。
func TestInstagramDownloadAuthError(t *testing.T) {
	root := t.TempDir()
	script := `#!/bin/sh
printf 'download started\n'
printf '[instagram][error] HTTP redirect to login page\n' >&2
exit 4
`
	if err := os.WriteFile(filepath.Join(root, "gallery-dl"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	err := (CLI{DataDir: root}).Download(context.Background(), domain.Subscription{Platform: "instagram"}, domain.Content{URL: "https://example.com/post", Asset: 1}, domain.YouTubeOptions{}, filepath.Join(root, "work"), func(string) {})
	var exitError *exec.ExitError
	if err == nil || !strings.Contains(err.Error(), "请在系统设置中编辑并保存有效 Cookie") || !errors.As(err, &exitError) {
		t.Fatalf("error=%v", err)
	}
}

// collectScan 在测试内收集交付内容，便于断言解析协议。
func collectScan(cli CLI, ctx context.Context, sub domain.Subscription, limit int, log func(string)) (domain.ScanResult, error) {
	var items []domain.Content
	result, err := cli.Scan(ctx, sub, limit, log, func(batch domain.ScanResult) error { items = append(items, batch.Items...); return nil })
	result.Items = items
	return result, err
}
