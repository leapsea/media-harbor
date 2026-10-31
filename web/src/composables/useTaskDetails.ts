import { onMounted, onUnmounted, ref, type Ref } from 'vue'
import { api } from '../stores/media'
import type { TaskDetails } from '../types'

/** 面板自行轮询，不扩大管理列表请求；串行请求避免慢响应覆盖新的进度。 */
/** history 是随详情返回的已结束内容数量；更早的记录由用户按需展开。 */
export function useTaskDetails(id: number, reportError: (error: unknown) => void, history: Ref<number>) {
 const details = ref<TaskDetails>({ platform: '', items: [], hidden: 0 })
 const loading = ref(true)
 let timer: ReturnType<typeof setTimeout> | undefined
 let disposed = false
 let failed = false
 /** 每次轮询独立取消；关闭立即终止网络请求和数据库等待。 */
 let controller: AbortController | undefined
 let sequence = 0
 async function refresh() {
  const mine = ++sequence
  controller = new AbortController()
  try {
   const next = await api<TaskDetails>(`/subscriptions/${id}/details?history=${history.value}`, 'GET', undefined, controller.signal)
   if (disposed || mine !== sequence) return
   details.value = next
   failed = false
  } catch (error) {
   if (mine !== sequence) return
   if (!disposed && !failed) reportError(error)
   failed = true
  } finally {
   if (!disposed && mine === sequence) { loading.value = false; timer = setTimeout(refresh, 1000) }
  }
 }
 /** 展开更早记录时立即重读：取消排定的轮询与在途请求，旧响应不再覆盖。 */
 async function reload() { clearTimeout(timer); controller?.abort(); await refresh() }
 onMounted(refresh)
 /** 关闭即停止请求，晚到的响应不能改变已关闭面板。 */
 onUnmounted(() => { disposed = true; clearTimeout(timer); controller?.abort() })
 /** 历史生命周期仅 clearTimeout(timer)，已经发出的请求仍会继续读取全量详情。 */
 return { details, loading, reload }
}
