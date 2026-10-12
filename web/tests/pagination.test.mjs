import test from 'node:test'
import assert from 'node:assert/strict'
import { pageWindow } from '../src/utils/pagination.ts'

// 分页边界用于下载历史和任务管理，列表数量变化不能留下空白末页。
test('空列表、完整页与末页范围', () => {
 assert.deepEqual(pageWindow(0,20,1), { pages:1,current:1,start:0,end:0 })
 assert.deepEqual(pageWindow(42,20,2), { pages:3,current:2,start:20,end:40 })
 assert.deepEqual(pageWindow(42,20,3), { pages:3,current:3,start:40,end:42 })
})
test('末页记录删除后回到有效页，条数控制切片边界', () => {
 assert.deepEqual(pageWindow(20,20,3), { pages:1,current:1,start:0,end:20 })
 assert.deepEqual(pageWindow(42,5,2), { pages:9,current:2,start:5,end:10 })
})

// 可控响应式列表模拟轮询与保存默认值，无需发起真实下载。
import { ref, nextTick } from 'vue'
import { usePagination } from '../src/composables/usePagination.ts'
test('轮询保留当前页，切换条数与系统默认值回到首页', async () => {
 const records = ref(Array.from({length:42},(_,id)=>id))
 const defaultSize = ref(20)
 const pager = usePagination(()=>records.value,()=>defaultSize.value)
 pager.page.value = 3; await nextTick()
 assert.deepEqual(pager.items.value,[40,41])
 records.value = [...records.value]; await nextTick()
 assert.equal(pager.page.value,3)
 records.value = records.value.slice(0,20); await nextTick()
 assert.equal(pager.page.value,1)
 pager.size.value = 5; await nextTick()
 pager.page.value = 2; await nextTick()
 defaultSize.value = 10; await nextTick()
 assert.equal(pager.size.value,10)
 assert.equal(pager.page.value,1)
})
