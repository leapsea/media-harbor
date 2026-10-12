import { onMounted, onUnmounted, ref } from 'vue'
import { api } from '../stores/media'
import type { TaskDetails } from '../types'

/** 面板自行轮询，不扩大管理列表请求；串行请求避免慢响应覆盖新的进度。 */
export function useTaskDetails(id: number, reportError: (error: unknown) => void) {
 const details = ref<TaskDetails>({ platform: '', items: [] })
 const loading = ref(true)
 let timer: ReturnType<typeof setTimeout> | undefined
 let disposed = false
 let failed = false
 async function refresh() {
  try {
   const next = await api<TaskDetails>(`/subscriptions/${id}/details`)
   if (disposed) return
   details.value = next
   failed = false
  } catch (error) {
   if (!disposed && !failed) reportError(error)
   failed = true
  } finally {
   if (!disposed) { loading.value = false; timer = setTimeout(refresh, 1000) }
  }
 }
 onMounted(refresh)
 /** 关闭即停止请求，晚到的响应不能改变已关闭面板。 */
 onUnmounted(() => { disposed = true; clearTimeout(timer) })
 return { details, loading }
}
