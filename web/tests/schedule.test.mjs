import { test } from 'node:test'
import assert from 'node:assert/strict'
import { parseSchedule, encodeSchedule } from '../src/utils/schedule.ts'

test('已保存的常见周期回显后保持原样', () => {
 for (const value of ['', '@every 6h', '@every 30m', '45 23 * * *', '15 8 * * 0', '0 2 * * 1']) {
  assert.equal(encodeSchedule(parseSchedule(value)), value)
 }
})

test('不能用控件表示的已有周期不被改写', () => {
 for (const value of ['0 2 1 * *', '@daily', '@every 90m', '*/15 * * * *']) {
  assert.equal(parseSchedule(value).mode, 'preserve')
  assert.equal(encodeSchedule(parseSchedule(value)), value)
 }
})

test('鼠标选择生成正确的间隔、日历和手动周期', () => {
 const choice = parseSchedule('@every 6h')
 assert.equal(encodeSchedule({ ...choice, interval: 12 }), '@every 12h')
 assert.equal(encodeSchedule({ ...choice, mode: 'daily', hour: 8, minute: 30 }), '30 8 * * *')
 assert.equal(encodeSchedule({ ...choice, mode: 'weekly', weekday: 5, hour: 20, minute: 15 }), '15 20 * * 5')
 assert.equal(encodeSchedule({ ...choice, mode: 'manual' }), '')
})
