import test from 'node:test'
import assert from 'node:assert/strict'
import { detailAnchor, largeFileBytes, statusDotPixels } from '../src/utils/taskDetails.ts'

const item = (key, status) => ({ key, status, title: key, parts: [] })
// 并发与等待内容保持原顺序，窗口从最早未结束项开始，完成后向后推进。
test('列表锚点允许待执行内容位于顶部，完成后向后推进', () => {
 assert.equal(detailAnchor([item('a','completed'), item('b','running'), item('c','queued')]), 'b')
 assert.equal(detailAnchor([item('a','completed'), item('b','completed'), item('c','queued')]), 'c')
 assert.equal(detailAnchor([item('a','queued'), item('b','running')]), 'a')
 assert.equal(detailAnchor([item('a','failed'), item('b','cancelled')]), '')
 assert.equal(detailAnchor([]), '')
 assert.equal(detailAnchor([item('a','completed'), item('later','queued')]), 'later')
})

test('大文件边界与整数状态点', () => {
 assert.equal(largeFileBytes, 5 * 1024 * 1024)
 assert.equal(statusDotPixels, 5)
})
