import test from 'node:test'
import assert from 'node:assert/strict'
import { confirmMutation } from '../src/utils/mutation.ts'

test('写入确认不等待刷新，刷新失败不撤销入队结果', async () => {
 const events = []
 let rejectRefresh
 const pending = new Promise((_, reject) => { rejectRefresh = reject })
 const result = await confirmMutation(
  async () => { events.push('queued') },
  () => { events.push('refresh'); return pending },
  () => events.push('success'),
  error => events.push(`write: ${error.message}`),
  error => events.push(error.message),
 )
 assert.equal(result, true)
 assert.deepEqual(events, ['queued', 'success', 'refresh'])
 rejectRefresh(new Error('refresh failed'))
 await Promise.resolve()
 assert.deepEqual(events, ['queued', 'success', 'refresh', 'refresh failed'])
})

test('写请求失败时不显示成功，也不启动刷新', async () => {
 const events = []
 const result = await confirmMutation(
  async () => { throw new Error('queue failed') },
  async () => { events.push('refresh') },
  () => events.push('success'),
  error => events.push(error.message),
  error => events.push(`refresh: ${error.message}`),
 )
 assert.equal(result, false)
 assert.deepEqual(events, ['queue failed'])
})
