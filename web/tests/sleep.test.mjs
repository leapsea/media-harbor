import test from 'node:test'
import assert from 'node:assert/strict'
import { withSleepEnabled } from '../src/utils/sleep.ts'

// 关闭不填写、不修复或删除草稿，用户仍可保存其他并发调度参数。
test('关闭休眠保留未填写及历史无效数据', () => {
 for (const window of [{ enabled: true, start: '', end: '', timezone: '' }, { enabled: true, start: 'NaN:NaN', end: 'invalid', timezone: 'invalid' }]) {
  assert.deepEqual(withSleepEnabled(window, false, 'Asia/Singapore'), { ...window, enabled: false })
 }
})

// 第一次开启得到可直接选择和保存的00:00–06:00窗口，时区来自当前浏览器。
test('首次开启自动填入项目默认时间与当前时区', () => {
 const window = { enabled: false, start: '', end: '', timezone: '' }
 assert.deepEqual(withSleepEnabled(window, true, 'Asia/Singapore'), { enabled: true, start: '00:00', end: '06:00', timezone: 'Asia/Singapore' })
 assert.deepEqual(window, { enabled: false, start: '', end: '', timezone: '' })
 assert.equal(withSleepEnabled(window, true).timezone, Intl.DateTimeFormat().resolvedOptions().timeZone)
})

// 部分填写仅补缺席字段；切换开关不把已选择的窗口重置为初始值。
test('已有休眠数据与时区在关闭和重新开启后保持不变', () => {
 const window = { enabled: true, start: '23:45', end: '07:15', timezone: 'Asia/Tokyo' }
 assert.deepEqual(withSleepEnabled(withSleepEnabled(window, false), true, 'UTC'), window)
 assert.deepEqual(withSleepEnabled({ ...window, end: '' }, true, 'UTC'), { ...window, end: '06:00' })
})
