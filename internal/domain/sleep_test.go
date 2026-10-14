package domain

import (
	"testing"
	"time"
)

// TestSleepBoundaries 验证日内、跨午夜、起止边界与配置时区。
func TestSleepBoundaries(t *testing.T) {
	// 起点恰好暂停，终点恰好恢复，窗口外返回零值。
	for _, tc := range []struct{ start, end, now, until string }{
		{"01:00", "06:00", "2026-10-14T00:59:59+08:00", ""},
		{"01:00", "06:00", "2026-10-14T01:00:00+08:00", "2026-10-14T06:00:00+08:00"},
		{"01:00", "06:00", "2026-10-14T06:00:00+08:00", ""},
		{"23:00", "07:00", "2026-10-14T23:30:00+08:00", "2026-10-15T07:00:00+08:00"},
		{"23:00", "07:00", "2026-10-15T00:30:00+08:00", "2026-10-15T07:00:00+08:00"},
		{"23:00", "07:00", "2026-10-15T07:00:00+08:00", ""},
		{"23:00", "07:00", "2026-10-14T15:30:00Z", "2026-10-15T07:00:00+08:00"},
	} {
		// 输入可来自UTC，期望恢复时刻仍采用设置时区。
		now, _ := time.Parse(time.RFC3339, tc.now)
		got := (SleepSettings{true, tc.start, tc.end, "Asia/Singapore"}).Until(now)
		if tc.until == "" {
			if !got.IsZero() {
				t.Fatal(tc, got)
			}
			continue
		}
		if got.Format(time.RFC3339) != tc.until {
			t.Fatal(tc, got)
		}
	}
}

// TestSleepValidation 禁止全天休眠和非法字段，关闭时兼容旧数据。
func TestSleepValidation(t *testing.T) {
	for _, s := range []SleepSettings{{true, "25:00", "06:00", "UTC"}, {true, "01:00", "01:00", "UTC"}, {true, "01:00", "06:00", "invalid"}, {true, "1:00", "06:00", "UTC"}} {
		if s.Validate() == nil {
			t.Fatal("接受无效配置", s)
		}
	}
	if err := (SleepSettings{}).Validate(); err != nil {
		t.Fatal(err)
	}
	// 秋季夏令时切换按本地日期恢复，不用固定24小时跨日。
	now, _ := time.Parse(time.RFC3339, "2026-10-31T23:30:00-04:00")
	got := (SleepSettings{true, "23:00", "07:00", "America/New_York"}).Until(now)
	if got.Format(time.RFC3339) != "2026-11-01T07:00:00-05:00" {
		t.Fatal(got)
	}
	// 重复小时的第二次01:20不能返回已过去的第一轮01:30。
	now, _ = time.Parse(time.RFC3339, "2026-11-01T01:20:00-05:00")
	got = (SleepSettings{true, "23:00", "01:30", "America/New_York"}).Until(now)
	if got.Format(time.RFC3339) != "2026-11-01T01:30:00-05:00" {
		t.Fatal(got)
	}
}
