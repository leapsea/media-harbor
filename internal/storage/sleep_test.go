package storage

import (
	"encoding/json"
	"media-harbor/internal/config"
	"path/filepath"
	"testing"
)

// TestSleepMigration 补充旧设置缺失字段，不覆盖既有下载参数和休眠选择。
func TestSleepMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "media.db")
	defaults := config.Defaults()
	s, err := Open(path, defaults)
	if err != nil {
		t.Fatal(err)
	}
	// 删除新字段模拟旧版本，保留用户修改后的并发值。
	// 写入旧payload后关闭连接，下一次打开才执行真实迁移。
	settings, _ := s.Settings()
	settings.YouTubeConcurrent = 7
	raw, _ := json.Marshal(settings)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	delete(fields, "sleep")
	raw, _ = json.Marshal(fields)
	if _, err = s.db.Exec("UPDATE app_settings SET payload=? WHERE id=1", raw); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path, defaults)
	if err != nil {
		t.Fatal(err)
	}
	// 升级默认关闭，不让已有部署在未选择窗口时突然休眠。
	got, _ := s.Settings()
	if got.Sleep != defaults.Sleep || got.YouTubeConcurrent != 7 {
		t.Fatal(got)
	}
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
