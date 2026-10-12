import { test } from 'node:test'
import assert from 'node:assert/strict'
import { taskMode } from '../src/utils/taskMode.ts'

/** 前后端使用相同分类：主页周期、详情单次，视频附带列表参数仍为详情。 */
test('主页和详情执行方式', () => {
 for (const [platform, url, mode] of [
  ['youtube', 'https://www.youtube.com/@author/videos', 'profile'],
  ['youtube', 'https://www.youtube.com/watch?v=one&list=PL1', 'content'],
  ['youtube', 'https://youtu.be/one', 'content'],
  ['youtube', 'https://www.youtube.com/shorts/one', 'content'],
  ['instagram', 'https://www.instagram.com/author/', 'profile'],
  ['instagram', 'https://www.instagram.com/author/reels/', 'profile'],
  ['instagram', 'https://www.instagram.com/p/one/', 'content'],
  ['instagram', 'https://www.instagram.com/reel/one/', 'content'],
 ]) assert.equal(taskMode(platform, url), mode)
})
