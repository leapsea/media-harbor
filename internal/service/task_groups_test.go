package service

import (
	"context"
	"fmt"
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"os"
	"strings"
	"testing"
	"time"
)

// manyEngine 用超过测试上限的枚举结果验证服务边界，而不是依赖平台。
// 十五项超过服务上限，用来检验入队边界而非命令参数。
type manyEngine struct{ fakeEngine }

func (manyEngine) Scan(_ context.Context, _ domain.Subscription, _ int, _ func(string), emit func(domain.ScanResult) error) (domain.ScanResult, error) {
	result := domain.ScanResult{StableID: "sample", CurrentName: "sample"}
	for i := 1; i <= 15; i++ {
		result.Items = append(result.Items, domain.Content{RemoteID: fmt.Sprint(i), URL: fmt.Sprintf("https://example.com/%d", i)})
	}
	return result, emit(result)
}

// TestLimitedScan 验证只入队十项，以及第二次扫描正确报告去重。
func TestLimitedScan(t *testing.T) {
	// 真实数据库保留两次扫描的记录，重复扫描必须遵循既有去重逻辑。
	a := testApp(t, manyEngine{})
	settings, _ := a.Repo.Settings()
	settings.Debug.TestItemLimit = 10
	if err := a.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	s := addSub(t, a, "sample", "instagram")
	var last domain.Task
	for i := 0; i < 2; i++ {
		var err error
		last, err = a.QueueScan(s.ID, "full")
		if err != nil {
			t.Fatal(err)
		}
		// 第一轮下载已在队列中，扫描会等待它们结束，不能假设立即被认领。
		for attempt := 0; attempt < 12; attempt++ {
			if err = a.tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			a.wg.Wait()
			current, err := a.Repo.Task(last.ID)
			if err != nil {
				t.Fatal(err)
			}
			if current.Status == "completed" {
				break
			}
		}
	}
	// 数据库数量是入队上限的证据，不只检查日志文字。
	contents, err := a.Repo.Contents()
	if err != nil || len(contents) != 10 {
		t.Fatalf("contents=%d err=%v", len(contents), err)
	}
	raw, err := os.ReadFile(a.LogPath(last))
	if err != nil {
		t.Fatal(err)
	}
	// 重复统计必须说明没有新增任务，避免用户误以为扫描未找到数据。
	for _, want := range []string{"仅获取前 10", "累计发现 10 项", "新增下载 0 项；已存在跳过 10 项", "本次执行结束"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("missing %s: %s", want, raw)
		}
	}
}

// TestGroupScope 验证旧的超量队列不会继续执行，批量操作不影响其他订阅。
func TestGroupScope(t *testing.T) {
	// 模拟升级前的十五条积压，验证开启测试模式后不再全部执行。
	a := testApp(t, fakeEngine{})
	settings, _ := a.Repo.Settings()
	settings.Debug.TestItemLimit = 10
	if err := a.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	s := addSub(t, a, "first", "instagram")
	other := addSub(t, a, "other", "youtube")
	for i := 1; i <= 15; i++ {
		if err := a.Repo.AddContentAndTask(s.ID, domain.Content{RemoteID: fmt.Sprint(i), URL: "https://example.com/media"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Repo.AddContentAndTask(other.ID, domain.Content{RemoteID: "other", URL: "https://example.com/other"}); err != nil {
		t.Fatal(err)
	}
	// 调度足够多轮，范围外五条仍应等待，而不是延迟几轮后被执行。
	for i := 0; i < 16; i++ {
		if err := a.tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		a.wg.Wait()
	}
	tasks, _ := a.Repo.Tasks()
	completed, queued := 0, 0
	for _, task := range tasks {
		if task.SubscriptionID != s.ID {
			continue
		}
		if task.Status == "completed" {
			completed++
		}
		if task.Status == "queued" {
			queued++
		}
	}
	if completed != 10 || queued != 5 {
		t.Fatalf("completed=%d queued=%d", completed, queued)
	}
	// 两个订阅各自一行；当前批次完成不受范围外积压数量影响。
	groups, err := a.TaskGroups()
	if err != nil || len(groups) != 2 {
		t.Fatalf("groups=%+v err=%v", groups, err)
	}
	for _, group := range groups {
		if group.SubscriptionID == s.ID && (group.Total != 10 || group.Status != "completed") {
			t.Fatalf("group=%+v", group)
		}
	}
	// 先将当前十条标记失败，再验证仅当前十条重试；范围外积压保持原样。
	for _, task := range tasks {
		if task.SubscriptionID == s.ID && task.Status == "completed" {
			task.Status = "failed"
			if err := a.Repo.SaveTask(&task); err != nil {
				t.Fatal(err)
			}
		}
	}
	// 批次重试保留原任务 ID，并严格限制在选中的十个下载。
	count, err := a.GroupAction(s.ID, "retry")
	if err != nil || count != 10 {
		t.Fatalf("retry=%d %v", count, err)
	}
	// 整订阅取消应包含范围外积压，否则关闭测试模式会意外恢复下载。
	count, err = a.GroupAction(s.ID, "cancel")
	if err != nil || count != 15 {
		t.Fatalf("cancel=%d %v", count, err)
	}
	tasks, _ = a.Repo.Tasks()
	for _, task := range tasks {
		if task.SubscriptionID == other.ID && task.Status != "completed" {
			t.Fatalf("other changed: %+v", task)
		}
	}
	// 聚合日志只包含本次十条，其他订阅及积压不能混入。
	log, err := a.GroupLog(s.ID)
	if err != nil || !strings.Contains(log, "测试模式：只处理前 10") || strings.Count(log, "—— 任务 #") != 10 {
		t.Fatalf("log=%s err=%v", log, err)
	}
}

// TestScanWaitingLog 验证慢扫描在尚未返回条目前仍有可见进度。
// 超时使用同一个 Context，退出后应停止进度写入并允许正常关闭日志。
func TestScanWaitingLog(t *testing.T) {
	a := testApp(t, fakeEngine{block: true})
	s := addSub(t, a, "slow", "instagram")
	task, err := a.QueueScan(s.ID, "full")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), config.ScanLogInterval+time.Second)
	defer cancel()
	if err = a.tick(ctx); err != nil {
		t.Fatal(err)
	}
	a.wg.Wait()
	raw, err := os.ReadFile(a.LogPath(task))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "正在枚举媒体，已等待") || !strings.Contains(string(raw), "本次执行结束") {
		t.Fatalf("log=%s", raw)
	}
}

// TestConciseProgressLog 流百分比由详情负责，日志保留具体错误。
func TestConciseProgressLog(t *testing.T) {
	raw := "[2026-10-12 17:28:33] [download] 99% ETA 00:01\n[2026-10-12 17:28:33] [download] 100.0% ETA 00:01\n[2026-10-12 17:28:34] [download] 99.7% ETA 00:01\n[2026-10-12 17:28:34] [download] 100% in 00:01:08\n[2026-10-12 17:28:34] ERROR: HTTP Error 403\n"
	got := conciseTaskLog(raw)
	if strings.Contains(got, "[download]") || !strings.Contains(got, "ERROR: HTTP Error 403") {
		t.Fatal(got)
	}
}

// TestDebugLimitSwitch 设置保存后统计和日志立即使用新范围，已有条目完整保留。
func TestDebugLimitSwitch(t *testing.T) {
	a := testApp(t, fakeEngine{})
	sub := addSub(t, a, "sample", "instagram")
	for i := range 15 {
		if err := a.Repo.AddContentAndTask(sub.ID, domain.Content{RemoteID: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, limit := range []int{10, 5, 0} {
		settings, _ := a.Repo.Settings()
		settings.Debug.TestItemLimit = limit
		if err := a.SaveSettings(settings); err != nil {
			t.Fatal(err)
		}
		groups, err := a.TaskGroups()
		want := 15
		if limit > 0 {
			want = limit
		}
		if err != nil || len(groups) != 1 || groups[0].Total != want || groups[0].TestItemLimit != limit {
			t.Fatal(groups, err)
		}
		log, err := a.GroupLog(sub.ID)
		if err != nil || strings.Contains(log, "测试模式") != (limit > 0) {
			t.Fatal(log, err)
		}
	}
	contents, _ := a.Repo.Contents()
	if len(contents) != 15 {
		t.Fatal("切换调试限制不应删除历史条目")
	}
}
