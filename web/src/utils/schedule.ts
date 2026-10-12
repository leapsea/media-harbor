/**
 * 将鼠标选择的周期转换为后端已支持的表达式，保持存储协议不变。
 */
export interface ScheduleChoice {
 mode: string
 interval: number
 unit: string
 hour: number
 minute: number
 weekday: number
 original: string
}

/**
 * 常见已有周期回显为控件；无法表示的旧表达式原样保留，避免编辑其他字段时改变计划。
 */
export function parseSchedule(value: string): ScheduleChoice {
 const choice = { mode: 'preserve', interval: 6, unit: 'h', hour: 2, minute: 0, weekday: 1, original: value }
 if (!value) return { ...choice, mode: 'manual' }
 const interval = /^@every (\d+)(m|h)$/.exec(value)
 if (interval && Number(interval[1]) >= 1 && Number(interval[1]) <= 60) {
  return { ...choice, mode: 'interval', interval: Number(interval[1]), unit: interval[2]! }
 }
 const calendar = /^(\d+) (\d+) \* \* (\*|[0-6])$/.exec(value)
 if (calendar && Number(calendar[1]) < 60 && Number(calendar[2]) < 24) {
  return { ...choice, mode: calendar[3] === '*' ? 'daily' : 'weekly', minute: Number(calendar[1]), hour: Number(calendar[2]), weekday: calendar[3] === '*' ? 1 : Number(calendar[3]) }
 }
 return choice
}

/**
 * 日历周期以服务所在时区执行；间隔周期从任务调度时刻开始计时。
 */
export function encodeSchedule(choice: ScheduleChoice): string {
 switch (choice.mode) {
  case 'manual': return ''
  case 'interval': return `@every ${choice.interval}${choice.unit}`
  case 'daily': return `${choice.minute} ${choice.hour} * * *`
  case 'weekly': return `${choice.minute} ${choice.hour} * * ${choice.weekday}`
  default: return choice.original
 }
}
