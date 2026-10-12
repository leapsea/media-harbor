import { computed, ref, watch } from 'vue'
import { pageWindow } from '../utils/pagination.ts'

/** 轮询保持当前页，默认值变化或切换条数回到首页，删除记录后自动收回有效页。 */
export function usePagination<T>(source: () => T[], defaultSize: () => number) {
 const page = ref(1)
 const size = ref(defaultSize())
 const window = computed(() => pageWindow(source().length, size.value, page.value))
 const items = computed(() => source().slice(window.value.start, window.value.end))
 watch(defaultSize, value => { size.value = value })
 watch(size, () => { page.value = 1 })
 watch(() => window.value.current, value => { page.value = value })
 return { page, size, window, items }
}
