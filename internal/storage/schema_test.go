package storage

import (
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// 新库直接建立当前模型；订阅配置、帖子任务和下载资源任务、文件集合和执行账本同时初始化。
// 明确表集合可发现误留的迁移版本表，也不依赖不存在的历史数据库。
func TestCurrentSchemaInitializesDirectly(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "media.db"), config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows, err := s.db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'")
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	sort.Strings(names)
	want := []string{"app_settings", "content_items", "download_history_cleared", "download_items", "engine_versions", "platform_runs", "post_tasks", "subscriptions", "tasks", "youtube_paid_blocks"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("tables=%v", names)
	}
	// 状态关系由仓储事务显式处理，新库不能生成隐式更新的触发器。
	var triggers int
	if err = s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='trigger'").Scan(&triggers); err != nil || triggers != 0 {
		t.Fatal(triggers, err)
	}
	settings, err := s.Settings()
	if err != nil || !reflect.DeepEqual(settings, config.Defaults()) {
		t.Fatal(settings, err)
	}
}

// 当前结构重新打开只恢复运行状态，不重建帖子任务与下载资源任务或重新解释阶段进度。
// 使用完成任务验证读取幂等性，避免把重启恢复与数据升级混为一谈。
func TestCurrentPostResourcesPersistAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "media.db")
	s, err := Open(path, config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	sub := domain.Subscription{Platform: "youtube", URL: "https://example.test/author", YouTube: &domain.YouTubeOptions{Video: true, Subtitles: true}}
	if err = s.SaveSubscription(&sub); err != nil {
		t.Fatal(err)
	}
	if err = s.AddContentAndTask(sub.ID, domain.Content{RemoteID: "video"}); err != nil {
		t.Fatal(err)
	}
	tasks, _ := s.Tasks()
	task := tasks[0]
	task.Status = "completed"
	task.Progress = []domain.TransferProgress{{Kind: "video", Status: "completed", BytesDone: 100, BytesTotal: 100}, {Kind: "subtitles", Status: "skipped"}}
	if err = s.SaveTask(&task); err != nil {
		t.Fatal(err)
	}
	before, err := s.Post(sub.ID, "video")
	if err != nil {
		t.Fatal(err)
	}
	children, err := s.DownloadItems(before.ID)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path, config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	after, err := s.Post(sub.ID, "video")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.DownloadItems(after.ID)
	if err != nil {
		t.Fatal(err)
	}
	// 不仅比较数量，还比较主键、不可用结果、字节和更新时间，发现启动回填。
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(children, again) {
		t.Fatalf("post before=%+v after=%+v children=%+v again=%+v", before, after, children, again)
	}
}
