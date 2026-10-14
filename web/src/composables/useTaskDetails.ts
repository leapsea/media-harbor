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
 /** 每次轮询独立取消；关闭立即终止网络请求和数据库等待。 */
 let controller: AbortController | undefined
 async function refresh() {
  controller = new AbortController()
  try {
   const next = await api<TaskDetails>(`/subscriptions/${id}/details`, 'GET', undefined, controller.signal)
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
 onUnmounted(() => { disposed = true; clearTimeout(timer); controller?.abort() })
 /** 历史生命周期仅 clearTimeout(timer)，已经发出的请求仍会继续读取全量详情。 */
 return { details, loading }
}
