import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import { computed, reactive } from 'vue'
import ts from '../node_modules/typescript/lib/typescript.js'

// 执行实际组件的脚本和Vue响应式模型，只省略DOM及PrimeVue渲染。
function picker(value) {
 const props = reactive({ modelValue: value }), events = []
 const file = fs.readFileSync(new URL('../src/components/HourMinutePicker.vue', import.meta.url), 'utf8')
 const source = file.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
 const code = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS } }).outputText
 const require = name => name === 'vue' ? { computed } : {}
 const state = new Function('require', 'exports', 'defineProps', 'defineEmits', code + '\nreturn {time,select}')(
  require, {}, () => props, () => (name, value) => { events.push({ name, value }); props.modelValue = value },
 )
 return { ...state, props, events }
}

// 空值、越界和已污染的表单值不能再成为Invalid Date，但有效HH:mm完整保留。
test('休眠时间仅接收有效的24小时HH:mm', () => {
 for (const value of ['', 'NaN:NaN', '24:00', '12:60', '01', undefined, null]) {
  assert.equal(picker(value).time.value, null, String(value))
 }
 for (const [value, hour, minute] of [['00:00', 0, 0], ['06:05', 6, 5], ['23:59', 23, 59]]) {
  const date = picker(value).time.value
  assert.equal(date.getHours(), hour)
  assert.equal(date.getMinutes(), minute)
 }
})

// Invalid Date也满足instanceof Date；回写保护阻止NaN传播，后续有效选择仍可恢复。
test('拒绝无效日期回写并允许恢复有效时间', () => {
 const view = picker('NaN:NaN')
 view.select(new Date(NaN))
 view.select(null)
 assert.deepEqual(view.events, [])
 view.select(new Date(2000, 0, 1, 23, 59))
 assert.equal(view.props.modelValue, '23:59')
 assert.equal(view.time.value.getHours(), 23)
 assert.equal(view.time.value.getMinutes(), 59)
 view.select(new Date(2000, 0, 1, 0, 0))
 assert.equal(view.props.modelValue, '00:00')
})
