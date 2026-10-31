import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import ts from '../node_modules/typescript/lib/typescript.js'

// 执行实际 composable，只替换 Vue 生命周期、网络与时钟，模拟慢接口和关闭弹窗。
// 不依赖真实等待或下载，也不把组件内部状态改成测试专用实现。
function panel(api) {
 const lifecycle = {}, timers = []
 const exports = {}
 const source = fs.readFileSync(new URL('../src/composables/useTaskDetails.ts', import.meta.url), 'utf8')
 const code = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS } }).outputText
 const require = name => name === 'vue' ? {
  ref: value => ({ value }),
  onMounted: callback => { lifecycle.mount = callback },
  onUnmounted: callback => { lifecycle.close = callback },
 } : { api }
 const schedule = (callback, delay) => { const timer = { callback, delay }; timers.push(timer); return timer }
 const clear = timer => { if (timer) timer.cancelled = true }
 new Function('require', 'exports', 'setTimeout', 'clearTimeout', code)(require, exports, schedule, clear)
 const errors = []
 const state = exports.useTaskDetails(42, error => errors.push(error), { value: 30 })
 return { ...lifecycle, state, timers, errors }
}

// 慢请求关闭后必须发出取消信号；不产生失败 Toast，也不启动下一轮请求。
test('关闭详情取消正在等待的请求', async () => {
 let signal
 const view = panel((_path, _method, _body, current) => {
  signal = current
  return new Promise((_resolve, reject) => current.addEventListener('abort', () => reject(new DOMException('closed', 'AbortError'))))
 })
 const reading = view.mount()
 assert.equal(signal.aborted, false)
 view.close()
 await reading
 assert.equal(signal.aborted, true)
 assert.equal(view.errors.length, 0)
 assert.equal(view.timers.length, 0)
})

// 下一轮只在上一轮结束后调度，继续保持实时进度；关闭时清理已排定的轮询。
test('详情轮询串行执行并随面板关闭停止', async () => {
 let finish
 const view = panel(() => new Promise(resolve => { finish = resolve }))
 const reading = view.mount()
 assert.equal(view.state.loading.value, true)
 assert.equal(view.timers.length, 0)
 const details = { platform: 'youtube', items: [{ key: 'video', parts: [] }] }
 finish(details)
 await reading
 assert.equal(view.state.details.value, details)
 assert.equal(view.state.loading.value, false)
 assert.equal(view.timers.length, 1)
 assert.equal(view.timers[0].delay, 1000)
 view.close()
 assert.equal(view.timers[0].cancelled, true)
})
