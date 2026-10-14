package storage

import (
	"media-harbor/internal/config"
	"path/filepath"
	"testing"
)

// 初始化直接保存完整休眠设置；重新打开不会重置已保存的窗口。
func TestSleepInitializationAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "media.db")
	defaults := config.Defaults()
	s, err := Open(path, defaults)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Settings()
	if err != nil || got.Sleep != defaults.Sleep {
		t.Fatal(got, err)
	}
	got.YouTubeConcurrent = 7
	got.Sleep.Enabled, got.Sleep.Start, got.Sleep.End = true, "23:00", "07:00"
	if err = s.SaveSettings(got); err != nil {
		t.Fatal(err)
	}
	s.Close()
	// 再次打开不能把已保存的时间段改回初始化默认值。
	s, err = Open(path, defaults)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// 保留用户时区与跨午夜窗口，不使用新默认覆盖。
	again, _ := s.Settings()
	if again.Sleep != got.Sleep {
		t.Fatal(again.Sleep)
	}
}
