import { test } from 'node:test'
import assert from 'node:assert/strict'
import { directoryNameFromUrl } from '../src/utils/directoryName.ts'

test('主页 URL 预填账号，频道 ID 等待元数据', () => {
 assert.equal(directoryNameFromUrl('https://www.youtube.com/@alice/videos'), 'alice')
 assert.equal(directoryNameFromUrl('https://www.youtube.com/channel/UC123/videos'), '')
 assert.equal(directoryNameFromUrl('https://www.instagram.com/alice/?hl=en'), 'alice')
})

test('内容链接不使用资源 ID 冒充作者', () => {
 assert.equal(directoryNameFromUrl('https://www.youtube.com/watch?v=video123'), '')
 assert.equal(directoryNameFromUrl('https://youtu.be/video123?t=30'), '')
 assert.equal(directoryNameFromUrl('https://www.youtube.com/playlist?list=PL123'), '')
 assert.equal(directoryNameFromUrl('https://www.youtube.com/shorts/video123'), '')
 assert.equal(directoryNameFromUrl('https://www.instagram.com/reel/ABC123/'), '')
})

test('空地址和尚未完成的地址不产生目录名称', () => {
 assert.equal(directoryNameFromUrl(''), '')
 assert.equal(directoryNameFromUrl('https://'), '')
})
