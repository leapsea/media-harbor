package service

import (
	"context"
	"errors"
	"media-harbor/internal/domain"
	"os"
	"reflect"
	"strings"
	"testing"
)

// orderedScanEngine 记录真正检查过的媒体，缓存之后的历史不能只在入队时跳过。
type orderedScanEngine struct {
	fakeEngine
	items   []domain.Content
	checked []string
	after   error
}

func (e *orderedScanEngine) Scan(ctx context.Context, _ domain.Subscription, _ int, _ func(string), emit func(domain.ScanResult) error) (domain.ScanResult, error) {
	result := domain.ScanResult{StableID: "author", CurrentName: "Author"}
	// 检查次数发生在 emit 之前，能捕获生产者越过边界继续取历史的错误。
	for _, item := range e.items {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		e.checked = append(e.checked, item.RemoteID)
		if err := emit(domain.ScanResult{StableID: result.StableID, CurrentName: result.CurrentName, Items: []domain.Content{item}}); err != nil {
			return result, err
		}
	}
	return result, e.after
}

// TestIncrementalAndCatchupBoundary 同一份最新到历史数据，验证三种操作各自的范围。
// 故意在缓存之后留一个历史缺项，增量不能补它，补全则必须越过缓存继续处理。
func TestIncrementalAndCatchupBoundary(t *testing.T) {
	for _, platform := range []string{"instagram", "youtube"} {
		// 同样的边界规则适用于两个平台，不依赖各自 CLI 的停止能力。
		for _, kind := range []string{"scan", "catchup", "full"} {
			t.Run(platform+"/"+kind, func(t *testing.T) {
				e := &orderedScanEngine{items: []domain.Content{{RemoteID: "latest-2"}, {RemoteID: "latest-1"}, {RemoteID: "cached"}, {RemoteID: "old-gap"}}}
				a := testApp(t, e)
				sub := addSub(t, a, kind, platform)
				// 原索引代表已缓存条目；缺失和失败应由补全处理。
				if err := a.Repo.AddContentAndTask(sub.ID, domain.Content{RemoteID: "cached"}); err != nil {
					t.Fatal(err)
				}
				contents, _ := a.Repo.Contents()
				cached := contents[0]
				cached.Status = "failed"
				if err := a.Repo.SaveContent(&cached); err != nil {
					t.Fatal(err)
				}
				tasks, _ := a.Repo.Tasks()
				original := tasks[0]
				// 保留旧下载任务身份，确认增量没有暗中重试它。
				original.Status = "failed"
				if err := a.Repo.SaveTask(&original); err != nil {
					t.Fatal(err)
				}
				scan, err := a.QueueScan(sub.ID, kind)
				if err != nil {
					t.Fatal(err)
				}
				scan.Status = "running"
				if err = a.Repo.SaveTask(&scan); err != nil {
					t.Fatal(err)
				}
				settings, _ := a.Repo.Settings()
				// 所有模式受同一调试上限约束，本例不枚举真实历史。
				settings.Debug.TestItemLimit = 10
				// 只执行扫描，保持历史失败任务原状，避免下载调度影响范围断言。
				a.execute(context.Background(), scan, sub, settings)
				current, _ := a.Repo.Task(scan.ID)
				// 主动达到缓存边界是完成，不能显示失败或取消。
				if current.Status != "completed" {
					t.Fatalf("扫描未正常完成：%+v", current)
				}
				want := []string{"latest-2", "latest-1", "cached", "old-gap"}
				if kind == "scan" {
					want = want[:3]
				}
				if !reflect.DeepEqual(e.checked, want) {
					t.Fatalf("实际请求范围 %v，期望 %v", e.checked, want)
				}
				contents, _ = a.Repo.Contents()
				// 缓冲通道中的新项必须全部入队，不能随引擎停止丢失。
				ids := map[string]bool{}
				for _, item := range contents {
					ids[item.RemoteID] = true
				}
				if !ids["latest-2"] || !ids["latest-1"] || ids["old-gap"] != (kind != "scan") {
					t.Fatalf("下载索引与扫描范围不符：%v", ids)
				}
				previous, _ := a.Repo.Task(original.ID)
				// 全量只发现新增，补全才重新排队已有失败媒体。
				if (previous.Status == "queued") != (kind == "catchup") {
					t.Fatalf("只有补全应恢复历史失败项：%+v", previous)
				}
				raw, _ := os.ReadFile(taskLogPathForTest(t, a, scan))
				if strings.Contains(string(raw), "命中本地缓存") != (kind == "scan") {
					t.Fatalf("扫描结束原因不明确：%s", raw)
				}
			})
		}
	}
}

// 本地索引的存在决定边界，已归档、历史隐藏和失败状态不触发增量补全。
func TestIncrementalBoundaryUsesExistingIndex(t *testing.T) {
	for _, status := range []string{"completed", "queued", "running", "failed", "cancelled", "skipped"} {
		t.Run(status, func(t *testing.T) {
			e := &orderedScanEngine{items: []domain.Content{{RemoteID: "cached"}, {RemoteID: "older"}}}
			a := testApp(t, e)
			sub := addSub(t, a, "cached-status", "instagram")
			_ = a.Repo.AddContentAndTask(sub.ID, domain.Content{RemoteID: "cached"})
			items, _ := a.Repo.Contents()
			item := items[0]
			item.Status, item.Archived = status, true
			// 索引存在即为边界，不读取或核验已经归档的文件。
			_ = a.Repo.SaveContent(&item)
			if _, err := a.Repo.ClearDownloadHistory(); err != nil {
				t.Fatal(err)
			}
			_, err := a.streamScan(context.Background(), sub, 10, "scan", func(string) {}, func(domain.ScanResult) error {
				t.Fatal("缓存媒体不应交给文件检查或入队消费者")
				return nil
			})
			if err != nil || !reflect.DeepEqual(e.checked, []string{"cached"}) {
				t.Fatalf("checked=%v err=%v", e.checked, err)
			}
		})
	}
}

// 本轮刚发现的重复项不能提前结束；其他配置的同名媒体不属于当前本地缓存。
func TestIncrementalUsesSnapshotPerSubscription(t *testing.T) {
	e := &orderedScanEngine{items: []domain.Content{{RemoteID: "new"}, {RemoteID: "new"}, {RemoteID: "next"}}}
	a := testApp(t, e)
	sub := addSub(t, a, "new-author", "youtube")
	other := addSub(t, a, "other-author", "youtube")
	// 远端 ID 的去重范围是当前配置，不是全站所有博主。
	_ = a.Repo.AddContentAndTask(other.ID, domain.Content{RemoteID: "new"})
	_, err := a.streamScan(context.Background(), sub, 10, "scan", func(string) {}, func(batch domain.ScanResult) error {
		// 重复项仍由仓储唯一约束去重，但不会改变开始时的停止快照。
		_, _, _, _, _, err := a.reconcileScanLocked(sub.ID, batch.Items, false)
		return err
	})
	items, _ := a.Repo.Contents()
	if err != nil || len(e.checked) != 3 || len(items) != 3 {
		t.Fatalf("重复导致增量提前停止：checked=%v items=%v err=%v", e.checked, items, err)
	}
}

// 真正的分页错误和主动取消仍返回错误，不能被正常缓存边界掩盖。
func TestIncrementalPreservesFailureAndCancellation(t *testing.T) {
	failure := errors.New("page failed")
	e := &orderedScanEngine{items: []domain.Content{{RemoteID: "new"}}, after: failure}
	a := testApp(t, e)
	sub := addSub(t, a, "partial-incremental", "youtube")
	_, err := a.streamScan(context.Background(), sub, 10, "scan", func(string) {}, func(domain.ScanResult) error { return nil })
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	// 取消的优先级高于正常增量结束，避免错误成功提示。
	cancel()
	_, err = a.streamScan(ctx, sub, 10, "scan", func(string) {}, func(domain.ScanResult) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
