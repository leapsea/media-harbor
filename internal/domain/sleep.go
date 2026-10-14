package domain

import (
	"fmt"
	"time"
)

// SleepSettings 描述每天的休眠窗口；起点包含、终点不包含，可跨午夜。
// 时区持久化，宿主机或浏览器时区变化不会改变用户选择的时间。
type SleepSettings struct {
	Enabled  bool   `json:"enabled"`
	Start    string `json:"start"`
	End      string `json:"end"`
	Timezone string `json:"timezone"`
}

// Validate 拒绝全天休眠和模糊时间；关闭功能不要求旧客户端提供字段。
func (s SleepSettings) Validate() error {
	if !s.Enabled {
		return nil
	}
	for _, value := range []string{s.Start, s.End} {
		if parsed, err := time.Parse("15:04", value); err != nil || parsed.Format("15:04") != value {
			if err == nil {
				return fmt.Errorf("休眠时间必须为 HH:mm")
			}
			return fmt.Errorf("休眠时间必须为 HH:mm：%w", err)
		}
	}
	if s.Start == s.End {
		return fmt.Errorf("休眠开始与结束时间不能相同")
	}
	if s.Timezone == "" {
		return fmt.Errorf("休眠时区不能为空")
	}
	if _, err := time.LoadLocation(s.Timezone); err != nil {
		return fmt.Errorf("休眠时区无效：%w", err)
	}
	return nil
}

// Until 在休眠时返回恢复时刻，按当地日历计算跨日与夏令时。
// 无效配置不会让队列永久停滞，写入前由应用服务完成校验。
func (s SleepSettings) Until(now time.Time) time.Time {
	if !s.Enabled || s.Validate() != nil {
		return time.Time{}
	}
	loc, _ := time.LoadLocation(s.Timezone)
	now = now.In(loc)
	start, _ := time.Parse("15:04", s.Start)
	end, _ := time.Parse("15:04", s.End)
	// 按当地墙上时间判定，不将窗口转换成固定 UTC 小时。
	clock := now.Hour()*60 + now.Minute()
	a, b := start.Hour()*60+start.Minute(), end.Hour()*60+end.Minute()
	if (a < b && (clock < a || clock >= b)) || (a > b && clock >= b && clock < a) {
		return time.Time{}
	}
	// 跨午夜的后半段当天恢复，前半段在下一当地日期恢复。
	day := now
	if a > b && clock >= a {
		day = day.AddDate(0, 0, 1)
	}
	until := time.Date(day.Year(), day.Month(), day.Day(), end.Hour(), end.Minute(), 0, 0, loc)
	// 秋季回拨重复小时可能选中已经过去的第一次终点，使用当前偏移核对第二次。
	if !until.After(now) {
		_, offset := now.Zone()
		candidate := time.Date(day.Year(), day.Month(), day.Day(), end.Hour(), end.Minute(), 0, 0, time.FixedZone("", offset)).In(loc)
		if candidate.After(now) && candidate.Hour() == end.Hour() && candidate.Minute() == end.Minute() {
			until = candidate
		}
	}
	return until
}
