package service

import (
	"context"
	"media-harbor/internal/domain"
	"reflect"
	"testing"
)

// gatedPostEngine 模拟 SDK 在请求详情前上报身份，解析计数发生在数据库决策之后。
type gatedPostEngine struct {
	fakeEngine
	parsed []string
	codes  []string
}

func (*gatedPostEngine) SupportsPostTasks() bool { return true }
func (e *gatedPostEngine) Scan(ctx context.Context, sub domain.Subscription, _ int, _ func(string), emit func(domain.ScanResult) error) (domain.ScanResult, error) {
	for _, code := range e.codes {
		address := "https://www.instagram.com/p/" + code + "/"
		action, err := domain.GatePost(ctx, domain.PostCandidate{RemoteID: code, URL: address})
		if err != nil {
			return domain.ScanResult{}, err
		}
		if action == "stop" {
			break
		}
		if action == "skip" {
			continue
		}
		e.parsed = append(e.parsed, code)
		for num, id := range []string{code + "-1", code + "-2"} {
			if err = emit(domain.ScanResult{Items: []domain.Content{{RemoteID: id, URL: address, Asset: num + 1, ExpectedAssets: 2}}}); err != nil {
				return domain.ScanResult{}, err
			}
		}
	}
	return domain.ScanResult{}, nil
}

// 第一张已完成但第二张缺失时必须继续；整帖完成后才可成为增量边界。
func TestPostGateChecksWholeManifestBeforeParsing(t *testing.T) {
	for _, kind := range []string{"full", "scan", "catchup"} {
		t.Run(kind, func(t *testing.T) {
			e := &gatedPostEngine{codes: []string{"partial", "done", "next"}}
			a := testApp(t, e)
			sub := addSub(t, a, "posts", "instagram")
			for _, item := range []domain.Content{{RemoteID: "partial-1", URL: "https://www.instagram.com/p/partial/", Asset: 1, ExpectedAssets: 2}, {RemoteID: "done-1", URL: "https://www.instagram.com/p/done/", Asset: 1, ExpectedAssets: 2}, {RemoteID: "done-2", URL: "https://www.instagram.com/p/done/", Asset: 2, ExpectedAssets: 2}} {
				if err := a.Repo.AddContentAndTask(sub.ID, item); err != nil {
					t.Fatal(err)
				}
			}
			tasks, _ := a.Repo.Tasks()
			for _, task := range tasks {
				task.Status = "completed"
				if err := a.Repo.SaveTask(&task); err != nil {
					t.Fatal(err)
				}
			}
			_, err := a.streamScan(context.Background(), sub, 10, kind, func(string) {}, func(batch domain.ScanResult) error {
				_, _, _, _, _, err := a.reconcileScanLocked(sub.ID, batch.Items, kind == "catchup")
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"partial", "next"}
			if kind == "scan" {
				want = []string{"partial"}
			}
			if kind == "catchup" || kind == "full" {
				want = []string{"partial", "done", "next"}
			}
			if !reflect.DeepEqual(e.parsed, want) {
				t.Fatalf("parsed=%v want=%v", e.parsed, want)
			}
		})
	}
}

// 执行时读取最新订阅配置，GUI/CLI 保存后排队任务不能继续使用调度认领时的旧配置。
type latestConfigEngine struct {
	fakeEngine
	sub     domain.Subscription
	options domain.YouTubeOptions
}

func (e *latestConfigEngine) Download(ctx context.Context, sub domain.Subscription, item domain.Content, o domain.YouTubeOptions, dir string, log func(string)) error {
	e.sub, e.options = sub, o
	return e.fakeEngine.Download(ctx, sub, item, o, dir, log)
}
func TestExecutionReloadsConfigurationFromA(t *testing.T) {
	e := &latestConfigEngine{}
	a := testApp(t, e)
	old := addSub(t, a, "latest-config", "youtube")
	_ = a.Repo.AddContentAndTask(old.ID, domain.Content{RemoteID: "video", URL: "https://www.youtube.com/watch?v=video"})
	tasks, _ := a.Repo.Tasks()
	task := tasks[0]
	task.Status = "running"
	_ = a.Repo.SaveTask(&task)
	settings, _ := a.Repo.Settings()
	current := old
	current.URL = "https://www.youtube.com/@changed"
	current.YouTube = &domain.YouTubeOptions{Video: true, Thumbnail: true}
	_ = a.Repo.SaveSubscription(&current)
	a.execute(context.Background(), task, old, settings)
	if e.sub.URL != current.URL || !e.options.Thumbnail {
		t.Fatalf("sub=%+v options=%+v", e.sub, e.options)
	}
}

// 详情只显示一个调试资源时，父状态和 2/3 完成数量仍直接来自帖子任务，不能局部推算完成。
func TestTaskDetailsReadsBAndCInsteadOfVisibleTaskTotals(t *testing.T) {
	a := testApp(t, fakeEngine{})
	sub := addSub(t, a, "detail-parent", "instagram")
	for num, id := range []string{"pk-1", "pk-2", "pk-3"} {
		if err := a.Repo.AddContentAndTask(sub.ID, domain.Content{RemoteID: id, URL: "https://www.instagram.com/p/post/", Asset: num + 1, ExpectedAssets: 3}); err != nil {
			t.Fatal(err)
		}
	}
	tasks, _ := a.Repo.Tasks()
	for _, task := range tasks {
		item, _ := a.Repo.Content(task.ContentID)
		if item.Asset <= 2 {
			task.Status = "completed"
			if err := a.Repo.SaveTask(&task); err != nil {
				t.Fatal(err)
			}
		}
	}
	settings, _ := a.Repo.Settings()
	settings.Debug.TestItemLimit = 1
	_ = a.Repo.SaveSettings(settings)
	detail, err := a.TaskDetails(context.Background(), sub.ID)
	if err != nil || len(detail.Items) != 1 {
		t.Fatalf("detail=%+v err=%v", detail, err)
	}
	item := detail.Items[0]
	if item.PostID == 0 || item.Expected != 3 || item.Completed != 2 || item.Status == "completed" || len(item.Parts) != 1 || item.Parts[0].ResourceID == 0 || item.Parts[0].Status != "completed" {
		t.Fatal(item)
	}
}

// 首次默认设置来自初始化；保存请求缺字段时不借用数据库旧值，也不改变原配置。
func TestCurrentSettingsRejectMissingFieldsWithoutBackfill(t *testing.T) {
	a := testApp(t, fakeEngine{})
	original, err := a.Repo.Settings()
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"pipeline", "sleep", "log_lines", "disabled_sleep_start"} {
		candidate := original
		switch field {
		case "pipeline":
			candidate.Pipeline = domain.PipelineSettings{}
		case "sleep":
			candidate.Sleep = domain.PlatformSleepSettings{}
		case "log_lines":
			candidate.UI.TaskLogLines = 0
		case "disabled_sleep_start":
			candidate.Sleep.Instagram.Enabled = false
			candidate.Sleep.Instagram.Start = ""
		}
		if err = a.SaveSettings(candidate); err == nil {
			t.Fatalf("缺席字段被旧值补齐：%s", field)
		}
		saved, _ := a.Repo.Settings()
		if !reflect.DeepEqual(saved, original) {
			t.Fatalf("无效请求修改了已保存配置：%s", field)
		}
	}
	// 当前完整结构仍可直接保存；关闭有效窗口不改变休眠功能的原有语义。
	candidate := original
	candidate.Sleep.Instagram.Enabled = false
	if err = a.SaveSettings(candidate); err != nil {
		t.Fatal(err)
	}
}
