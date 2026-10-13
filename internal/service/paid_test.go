package service

import (
	"context"
	"fmt"
	"media-harbor/internal/domain"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// paidEngine 用可控结果验证频道隔离，实际调用次数证明跳过不是一次失败下载。
type paidEngine struct {
	fakeEngine
	mu    sync.Mutex
	calls []string
	fail  bool
}

func (e *paidEngine) Download(ctx context.Context, s domain.Subscription, c domain.Content, o domain.YouTubeOptions, dir string, log func(string)) error {
	e.mu.Lock()
	e.calls = append(e.calls, c.RemoteID)
	fail := e.fail && c.RemoteID == "first"
	e.mu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if fail {
		return fmt.Errorf("会员视频无法下载")
	}
	return e.fakeEngine.Download(ctx, s, c, o, dir, log)
}

func TestPaidVideoSkipAndManualRetry(t *testing.T) {
	e := &paidEngine{fail: true}
	a := testApp(t, e)
	settings, _ := a.Repo.Settings()
	settings.YouTubeConcurrent = 1
	_ = a.SaveSettings(settings)
	first := addSub(t, a, "first-config", "youtube")
	peer := addSub(t, a, "same-channel", "youtube")
	other := addSub(t, a, "other-channel", "youtube")
	for _, entry := range []struct {
		sid                       int64
		id, channel, availability string
	}{
		{first.ID, "first", "UCsame", "subscriber_only"}, {peer.ID, "later", "UCsame", "subscriber_only"},
		{first.ID, "public", "UCsame", "public"}, {other.ID, "other", "UCother", "subscriber_only"},
	} {
		_ = a.Repo.AddContentAndTask(entry.sid, domain.Content{RemoteID: entry.id, URL: "https://example.com/" + entry.id, ChannelID: entry.channel, Availability: entry.availability})
	}
	run := func() {
		for i := 0; i < 5; i++ {
			if err := a.tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			a.wg.Wait()
		}
	}
	run()
	items, _ := a.Repo.Contents()
	statuses := map[string]string{}
	for _, item := range items {
		statuses[item.RemoteID] = item.Status
	}
	if statuses["first"] != "failed" || statuses["later"] != "skipped" || statuses["public"] != "completed" || statuses["other"] != "completed" {
		t.Fatal(statuses)
	}
	e.mu.Lock()
	calls := strings.Join(e.calls, ",")
	e.mu.Unlock()
	if strings.Contains(calls, "later") {
		t.Fatal(calls)
	}
	blocked, _ := a.Repo.PaidFailure("UCsame")
	if blocked == 0 {
		t.Fatal("policy not persisted")
	}
	log, err := a.GroupLog(peer.ID)
	if err != nil || !strings.Contains(log, "跳过付费帖子") || !strings.Contains(log, "跳过付费视频") {
		t.Fatalf("%s %v", log, err)
	}
	tasks, _ := a.Repo.Tasks()
	for _, task := range tasks {
		if task.SubscriptionID == peer.ID {
			if task.Attempts != 0 {
				t.Fatal(task)
			}
			if _, err := os.Stat(a.WorkPath(task)); !os.IsNotExist(err) {
				t.Fatal("skipped task created work directory")
			}
		}
	}
	// 新应用实例不能绕过持久策略，新发现的同频道付费条目仍然跳过。
	a = New(a.Config, a.Repo, e)
	_ = a.Repo.AddContentAndTask(peer.ID, domain.Content{RemoteID: "later-new", ChannelID: "UCsame", Availability: "premium_only"})
	run()
	// 补全不清除策略，也不重新排队最初的失败付费视频。
	a.mu.Lock()
	_, retried, _, _, skipped, err := a.reconcileScanLocked(first.ID, []domain.Content{{RemoteID: "first", ChannelID: "UCsame", Availability: "subscriber_only"}}, true)
	a.mu.Unlock()
	if err != nil || retried != 0 || skipped != 1 {
		t.Fatalf("retried=%d skipped=%d err=%v", retried, skipped, err)
	}
	if block, _ := a.Repo.PaidFailure("UCsame"); block != blocked {
		t.Fatal("catchup cleared policy")
	}
	// 用户在任务管理重试恢复失败与跳过条目；成功后可继续下载付费内容。
	e.mu.Lock()
	e.fail = false
	e.mu.Unlock()
	if n, err := a.GroupAction(first.ID, "retry"); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	if block, _ := a.Repo.PaidFailure("UCsame"); block != 0 {
		t.Fatal("manual retry did not clear policy")
	}
	run()
	if n, err := a.GroupAction(peer.ID, "retry"); err != nil || n != 2 {
		t.Fatalf("%d %v", n, err)
	}
	run()
	items, _ = a.Repo.Contents()
	for _, item := range items {
		if item.Status != "completed" {
			t.Fatal(item)
		}
	}
}

func TestPublicFailureDoesNotBlockPaid(t *testing.T) {
	e := &paidEngine{fail: true}
	a := testApp(t, e)
	s := addSub(t, a, "public-failure", "youtube")
	_ = a.Repo.AddContentAndTask(s.ID, domain.Content{RemoteID: "first", ChannelID: "UCsame", Availability: "public"})
	_ = a.tick(context.Background())
	a.wg.Wait()
	if blocked, err := a.Repo.PaidFailure("UCsame"); err != nil || blocked != 0 {
		t.Fatalf("%d %v", blocked, err)
	}
}

func TestCancelledPaidDoesNotBlock(t *testing.T) {
	a := testApp(t, &paidEngine{fail: true})
	s := addSub(t, a, "cancelled-paid", "youtube")
	_ = a.Repo.AddContentAndTask(s.ID, domain.Content{RemoteID: "first", ChannelID: "UCsame", Availability: "subscriber_only"})
	tasks, _ := a.Repo.Tasks()
	task := tasks[0]
	task.Status = "running"
	_ = a.Repo.SaveTask(&task)
	settings, _ := a.Repo.Settings()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.execute(ctx, task, s, settings)
	if blocked, err := a.Repo.PaidFailure("UCsame"); err != nil || blocked != 0 {
		t.Fatalf("%d %v", blocked, err)
	}
}

// 详情地址在下载前才识别付费身份时，也归为跳过，而不是失败或成功。
type resolvedPaidEngine struct{ fakeEngine }

func (resolvedPaidEngine) Download(_ context.Context, _ domain.Subscription, _ domain.Content, _ domain.YouTubeOptions, dir string, _ func(string)) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, ".metadata.json"), []byte(`{"channel_id":"UCresolved","availability":"subscriber_only"}`), 0644); err != nil {
		return err
	}
	return &domain.PaidVideoSkippedError{ChannelID: "UCresolved", FailedTaskID: 42}
}
func TestResolvedPaidSkip(t *testing.T) {
	a := testApp(t, resolvedPaidEngine{})
	s := addSub(t, a, "resolve-paid", "youtube")
	_ = a.Repo.BlockPaidVideos("UCresolved", 42)
	_ = a.Repo.AddContentAndTask(s.ID, domain.Content{RemoteID: "unknown-access"})
	_ = a.tick(context.Background())
	a.wg.Wait()
	tasks, _ := a.Repo.Tasks()
	content, _ := a.Repo.Content(tasks[0].ContentID)
	if tasks[0].Status != "skipped" || content.Status != "skipped" || content.ChannelID != "UCresolved" || content.Availability != "subscriber_only" {
		t.Fatalf("%+v %+v", tasks, content)
	}
	if _, err := os.Stat(a.WorkPath(tasks[0])); !os.IsNotExist(err) {
		t.Fatal("metadata-only skip left work directory")
	}
	detail, err := a.TaskDetails(s.ID)
	if err != nil || detail.Items[0].Status != "skipped" || detail.Items[0].Error == "" || detail.Items[0].Parts[0].Status != "skipped" {
		t.Fatalf("%+v %v", detail, err)
	}
	groups, _ := a.TaskGroups()
	if groups[0].Status != "skipped" || groups[0].Counts["skipped"] != 1 || groups[0].Counts["completed"] != 0 || groups[0].Counts["failed"] != 0 {
		t.Fatal(groups)
	}
}

// 详情的视频 ID 不能被误当成频道；只有主页的稳定频道身份可作为补充。
func TestPaidChannelIdentity(t *testing.T) {
	video := domain.Subscription{Platform: "youtube", URL: "https://www.youtube.com/watch?v=UCvideo", StableID: "UCvideo"}
	if paidChannel(domain.Content{}, video) != "" {
		t.Fatal("video ID used as channel")
	}
	profile := domain.Subscription{Platform: "youtube", URL: "https://www.youtube.com/channel/UCauthor", StableID: "UCauthor"}
	if paidChannel(domain.Content{}, profile) != "UCauthor" {
		t.Fatal("missing profile channel")
	}
	if paidChannel(domain.Content{ChannelID: "UCactual"}, profile) != "UCactual" {
		t.Fatal("content channel not preferred")
	}
}
