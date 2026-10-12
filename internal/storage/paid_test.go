package storage

import (
	"media-harbor/internal/config"
	"path/filepath"
	"testing"
)

// 实际关闭并重开 SQLite，验证频道策略独立于进程和任务展示生命周期。
func TestPaidBlockPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paid.db")
	db, err := Open(path, config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	if err = db.BlockPaidVideos("UCpaid", 42); err != nil {
		t.Fatal(err)
	}
	_ = db.BlockPaidVideos("UCpaid", 43)
	_ = db.Close()
	db, err = Open(path, config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if id, err := db.PaidFailure("UCpaid"); err != nil || id != 42 {
		t.Fatalf("%d %v", id, err)
	}
	if id, err := db.PaidFailure("UCother"); err != nil || id != 0 {
		t.Fatalf("%d %v", id, err)
	}
	if err = db.AllowPaidVideos("UCpaid"); err != nil {
		t.Fatal(err)
	}
	if id, err := db.PaidFailure("UCpaid"); err != nil || id != 0 {
		t.Fatalf("%d %v", id, err)
	}
}
