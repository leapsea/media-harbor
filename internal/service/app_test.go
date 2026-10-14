package service

import (
	"context"
	"errors"
	"fmt"
	"media-harbor/internal/config"
	"media-harbor/internal/credentials"
	"media-harbor/internal/domain"
	"media-harbor/internal/storage"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeEngine 提供确定的扫描和下载结果，不调用真实平台。
type fakeEngine struct {
	block     bool
	scanError error
}

// 阻塞引擎用于观察运行并发和 Context 取消。
func (f fakeEngine) Scan(ctx context.Context, s domain.Subscription, limit int, log func(string), emit func(domain.ScanResult) error) (domain.ScanResult, error) {
	if f.block {
		<-ctx.Done()
		return domain.ScanResult{}, ctx.Err()
	}
	result := domain.ScanResult{StableID: "platform-123", CurrentName: "renamed", Items: []domain.Content{{RemoteID: "one", URL: "https://example.com/one", Published: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)}}}
	if err := emit(result); err != nil {
		return result, err
	}
	return result, f.scanError
}

// 测试媒体内容只用于文件移动断言，无需解码。
func (f fakeEngine) Download(ctx context.Context, s domain.Subscription, c domain.Content, o domain.YouTubeOptions, dir string, log func(string)) error {
	if f.block {
		<-ctx.Done()
		return ctx.Err()
	}
	if e := os.MkdirAll(dir, 0755); e != nil {
		return e
	}
	return os.WriteFile(filepath.Join(dir, "one.mp4"), []byte("media"), 0644)
}

// testApp 在测试专属目录中装配真实 SQLite 和可控引擎。
func testApp(t *testing.T, eng domain.Engine) *App {
	t.Helper()
	root := t.TempDir()
	c := config.Config{DataDir: filepath.Join(root, "data"), LogDir: filepath.Join(root, "logs"), DownloadDir: filepath.Join(root, "downloads"), ArchiveDir: filepath.Join(root, "archive")}
	for _, dir := range []string{c.DataDir, c.LogDir} {
		if e := os.MkdirAll(dir, 0755); e != nil {
			t.Fatal(e)
		}
	}
	db, e := storage.Open(filepath.Join(c.DataDir, "media.db"), config.Defaults())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	// 离线引擎使用测试凭据，认证规则不依赖真实账号。
	if err := credentials.New(c.DataDir).Save("instagram", "# Netscape HTTP Cookie File\n.instagram.com\tTRUE\t/\tTRUE\t0\tsessionid\toffline-test\n"); err != nil {
		t.Fatal(err)
	}
	return New(c, db, eng)
}

// addSub 为测试创建固定名称的手动订阅。
func addSub(t *testing.T, a *App, name, platform string) domain.Subscription {
	t.Helper()
	s := domain.Subscription{Platform: platform, URL: "https://example.com/" + name, DisplayName: name, Enabled: true}
	if e := a.SaveSubscription(&s); e != nil {
		t.Fatal(e)
	}
	return s
}

// taskLogPathForTest resolves the shared subscription log used by tasks created during a test.
func taskLogPathForTest(t *testing.T, a *App, task domain.Task) string {
	t.Helper()
	sub, err := a.Repo.Subscription(task.SubscriptionID)
	if err != nil {
		t.Fatal(err)
	}
	return a.LogPath(sub, time.Now())
}

// TestScanDownloadArchive 覆盖扫描、增量去重、稳定目录和恢复归档。
func TestScanDownloadArchive(t *testing.T) {
	// 使用真实数据库，核心流程无需模拟持久化语义。
	a := testApp(t, fakeEngine{})
	// 本测试固定年月日目录模板，不依赖全局默认模板的展示调整。
	settings, err := a.Repo.Settings()
	if err != nil {
		t.Fatal(err)
	}
	settings.Template = "{platform}/{directory_name}/{year}/{month}/{day}/{filename}"
	if err = a.Repo.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	s := addSub(t, a, "original", "youtube")
	if _, e := a.QueueScan(s.ID, "full"); e != nil {
		t.Fatal(e)
	}
	if e := a.tick(context.Background()); e != nil {
		t.Fatal(e)
	}
	a.wg.Wait()
	// 扫描改变当前名称，不改变固定目录名称。
	latest, e := a.Repo.Subscription(s.ID)
	if e != nil {
		t.Fatal(e)
	}
	if latest.DisplayName != "original" || latest.CurrentName != "renamed" || latest.StableID != "platform-123" {
		t.Fatalf("%+v", latest)
	}
	if e = a.tick(context.Background()); e != nil {
		t.Fatal(e)
	}
	a.wg.Wait()
	items, _ := a.Repo.Contents()
	c := items[0]
	// 文件日期来自内容发布日期，名称来自首次保存值。
	want := filepath.Join(a.Config.DownloadDir, "youtube/original/2026/01/02/one.mp4")
	if c.Status != "completed" || len(c.Paths) != 1 || c.Paths[0] != want {
		t.Fatalf("%+v", c)
	}
	// 再次扫描同一内容，任务总数只增加一条扫描。
	if _, e = a.QueueScan(s.ID, "scan"); e != nil {
		t.Fatal(e)
	}
	if e = a.tick(context.Background()); e != nil {
		t.Fatal(e)
	}
	a.wg.Wait()
	// 检查活动平台分布，而不是只检查调度器配置值。
	tasks, _ := a.Repo.Tasks()
	if len(tasks) != 3 {
		t.Fatalf("增量重复下载: %v", tasks)
	}
	// 原归档配置在目录失效后仍保留，恢复挂载后自动处理。
	latest.Archive = true
	if e = a.Repo.SaveSubscription(&latest); e != nil {
		t.Fatal(e)
	}
	if e = a.tick(context.Background()); e != nil {
		t.Fatal(e)
	}
	c, _ = a.Repo.Content(c.ID)
	if c.Archived {
		t.Fatal("未挂载时发生归档")
	}
	// 模拟卷恢复，使用相同内容记录继续归档。
	a.directories[2].Mounted = "mounted"
	if e = a.tick(context.Background()); e != nil {
		t.Fatal(e)
	}
	c, _ = a.Repo.Content(c.ID)
	if !c.Archived {
		t.Fatal("恢复后未归档")
	}
	if _, e = os.Stat(want); !os.IsNotExist(e) {
		t.Fatal("源文件仍保留")
	}
	if _, e = os.Stat(c.Paths[0]); e != nil {
		t.Fatal(e)
	}
}

// TestConcurrencyAndCancel 验证平台上限、取消状态和可重试行为。
func TestConcurrencyAndCancel(t *testing.T) {
	// 所有引擎先阻塞，便于确定实际活动任务数量。
	a := testApp(t, fakeEngine{block: true})
	settings, _ := a.Repo.Settings()
	settings.YouTubeConcurrent = 1
	_ = a.SaveSettings(settings)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	yt1 := addSub(t, a, "yt1", "youtube")
	yt2 := addSub(t, a, "yt2", "youtube")
	ig := addSub(t, a, "ig", "instagram")
	// 两条 YouTube 争用平台上限，Instagram 独立运行。
	for _, s := range []domain.Subscription{yt1, yt2, ig} {
		if _, e := a.QueueScan(s.ID, "scan"); e != nil {
			t.Fatal(e)
		}
	}
	if e := a.tick(ctx); e != nil {
		t.Fatal(e)
	}
	if len(a.active) != 2 {
		t.Fatalf("活动任务=%d", len(a.active))
	}
	tasks, _ := a.Repo.Tasks()
	running := map[string]int{}
	var target int64
	for _, v := range tasks {
		if v.Status == "running" {
			s, _ := a.Repo.Subscription(v.SubscriptionID)
			running[s.Platform]++
			if s.Platform == "youtube" {
				target = v.ID
			}
		}
	}
	if running["youtube"] != 1 || running["instagram"] != 1 {
		t.Fatal(running)
	}
	// 用户取消应保持 cancelled，服务取消则回到 queued。
	if e := a.CancelTask(target); e != nil {
		t.Fatal(e)
	}
	cancel()
	a.wg.Wait()
	task, _ := a.Repo.Task(target)
	if task.Status != "cancelled" {
		t.Fatal(task)
	}
	// 手动重试等待后台完全结束后再入队。
	if e := a.RetryTask(target); e != nil {
		t.Fatal(e)
	}
	task, _ = a.Repo.Task(target)
	if task.Status != "queued" {
		t.Fatal(task)
	}
}

// TestDownloadUnavailable 和暂停测试只覆盖核心目录行为。
func TestDownloadUnavailable(t *testing.T) {
	a := testApp(t, fakeEngine{})
	s := addSub(t, a, "alice", "youtube")
	if e := a.Repo.AddContentAndTask(s.ID, domain.Content{RemoteID: "one"}); e != nil {
		t.Fatal(e)
	}
	// 下载不可写时保留队列，不能认领为 running。
	a.directories[1].Writable = false
	if e := a.tick(context.Background()); e != nil {
		t.Fatal(e)
	}
	tasks, _ := a.Repo.Tasks()
	if tasks[0].Status != "queued" {
		t.Fatal(tasks)
	}
}

// TestPartialScan 保留已发现内容，同时记录扫描失败。
func TestPartialScan(t *testing.T) {
	// 平台分页失败仍返回已发现项。
	a := testApp(t, fakeEngine{scanError: errors.New("历史分页失败")})
	s := addSub(t, a, "alice", "youtube")
	task, e := a.QueueScan(s.ID, "full")
	if e != nil {
		t.Fatal(e)
	}
	if e = a.tick(context.Background()); e != nil {
		t.Fatal(e)
	}
	a.wg.Wait()
	task, _ = a.Repo.Task(task.ID)
	// 扫描失败和发现内容是两个独立可观察结果。
	if task.Status != "failed" {
		t.Fatal(task)
	}
	items, _ := a.Repo.Contents()
	if len(items) != 1 {
		t.Fatal(items)
	}
	s, _ = a.Repo.Subscription(s.ID)
	// 不允许用部分结果推进最近成功扫描时间。
	if !s.LastSuccess.IsZero() {
		t.Fatal("部分扫描被标为成功")
	}
}

// TestSchedules 验证 Cron、固定周期和纯手动订阅。
func TestSchedules(t *testing.T) {
	// 固定测试时间避免跨午夜影响周期断言。
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	for _, v := range []string{"@every 6h", "0 2 * * *", "@daily"} {
		next, e := NextRun(v, now)
		if e != nil || !next.After(now) {
			t.Fatalf("%s %v %v", v, next, e)
		}
	}
	// 空周期与错误表达式应分别对应手动和配置失败。
	if next, e := NextRun("", now); e != nil || !next.IsZero() {
		t.Fatal(next, e)
	}
	if _, e := NextRun("invalid", now); e == nil {
		t.Fatal("接受无效周期")
	}
}

// TestPlatformConcurrencyWithoutGlobalLimit 验证各平台只采集一个博主且互不阻挡。
// 下载并发再高也不能同时启动第二个 Instagram 博主。
func TestPlatformConcurrencyWithoutGlobalLimit(t *testing.T) {
	a := testApp(t, fakeEngine{block: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); a.wg.Wait() }()
	for i := 0; i < 11; i++ {
		s := addSub(t, a, fmt.Sprintf("ig-%d", i), "instagram")
		if _, err := a.QueueScan(s.ID, "scan"); err != nil {
			t.Fatal(err)
		}
	}
	yt := addSub(t, a, "youtube", "youtube")
	if _, err := a.QueueScan(yt.ID, "scan"); err != nil {
		t.Fatal(err)
	}
	if err := a.tick(ctx); err != nil {
		t.Fatal(err)
	}
	tasks, _ := a.Repo.Tasks()
	running := 0
	for _, task := range tasks {
		if task.Status == "running" {
			running++
		}
	}
	if running != 2 {
		t.Fatalf("running=%d, want=2", running)
	}
}

// TestDirectoryNameTemplate 使用固定目录名称，而非作者当前名称。
func TestDirectoryNameTemplate(t *testing.T) {
	sub := domain.Subscription{Platform: "youtube", DisplayName: "我的目录", CurrentName: "作者新名称"}
	content := domain.Content{Published: time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)}
	want := "youtube/我的目录/2026/1011/video.webm"
	got := renderPathAt("{platform}/{directory_name}/{year}/{month}{day}/{filename}", sub, content, "video.webm", content.Published)
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

// 默认归档只按挂载启用，保存和读取都关闭未挂载状态。
func TestDefaultArchiveFollowsMount(t *testing.T) {
	a := testApp(t, fakeEngine{})
	for _, tt := range []struct {
		mount          string
		writable, want bool
	}{
		{"unmounted", true, false}, {"unknown", true, false}, {"mounted", false, true}, {"mounted", true, true},
	} {
		a.directories[2].Mounted = tt.mount
		a.directories[2].Writable = tt.writable
		s := config.Defaults()
		s.AutoArchive = true
		if err := a.SaveSettings(s); err != nil {
			t.Fatal(err)
		}
		got, err := a.Settings()
		if err != nil || got.AutoArchive != tt.want {
			t.Fatalf("%s writable=%v: %v, %v", tt.mount, tt.writable, got.AutoArchive, err)
		}
		stored, _ := a.Repo.Settings()
		if stored.AutoArchive != tt.want {
			t.Fatal("saved archive default differs from mount rule")
		}
	}
	// 已挂载时，用户手动关闭后不会因读取设置而重新开启。
	s, _ := a.Settings()
	s.AutoArchive = false
	if err := a.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	got, _ := a.Settings()
	if got.AutoArchive {
		t.Fatal("manual archive choice was overwritten")
	}
}
