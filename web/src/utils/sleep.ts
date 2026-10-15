import type { SleepSettings } from '../types'

/** 首次开启采用项目的每日休眠初值；已有字段由用户持有，不覆盖或清空。 */
const defaultSleepTimes = { start: '00:00', end: '06:00' }

/** 开关切换一次性提交窗口，避免控件先启用、时间尚未补齐的中间状态。 */
export function withSleepEnabled(window: SleepSettings, enabled: boolean, timezone = Intl.DateTimeFormat().resolvedOptions().timeZone): SleepSettings {
 if (!enabled) return { ...window, enabled }
 return {
  ...window, enabled,
  start: window.start || defaultSleepTimes.start,
  end: window.end || defaultSleepTimes.end,
  timezone: window.timezone || timezone,
 }
}
