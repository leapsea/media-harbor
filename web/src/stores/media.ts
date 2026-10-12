import { t, setLocale } from '../i18n'
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { useToast } from 'primevue/usetoast'
import type { Dashboard, Subscription, Content, Settings, TaskGroup } from '../types'

/**
 * 同源请求只在这里处理错误，组件不重复实现数据读取逻辑。
 */
export async function api<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
 const response = await fetch(`/api${path}`, {
  method, headers: { 'Content-Type': 'application/json' },
  body: body === undefined ? undefined : JSON.stringify(body),
 })
 const data = await response.json()
 if (!response.ok) throw new Error(data.error || '请求失败')
 return data as T
}

/**
 * 状态与操作分离：刷新只读，mutate 在操作结束后统一刷新。
 */
export const useMediaStore = defineStore('media', () => {
 const toast = useToast()
 const dashboard = ref<Dashboard | null>(null)
 const subscriptions = ref<Subscription[]>([])
 const taskGroups = ref<TaskGroup[]>([])
 const contents = ref<Content[]>([])
 const settings = ref<Settings | null>(null)
 const busy = ref(false)
 let languageLoaded = false
 /** 归档入口和任务表单共享当前目录状态，避免页面各自判断。 */
 const archiveMounted = computed(() => dashboard.value?.directories[2]?.mounted === 'mounted')
 const archiveAvailable = computed(() => archiveMounted.value && dashboard.value?.directories[2]?.writable)
 /** 记录显示名统一由任务配置提供，配置已删除时保留任务 ID。 */
 function subscriptionName(id: number) { return subscriptions.value.find(s => s.id === id)?.display_name || t('任务 #{id}', { id }) }

 /**
 * 请求失败统一以浮动通知呈现，弹窗和页面共享同一个 Toast 宿主。
 */
 function reportError(error: unknown) {
  toast.add({ severity: 'error', summary: t('操作失败'), detail: t(error instanceof Error ? error.message : String(error)), life: 8000 })
 }

 /**
 * 保存前后的完整快照由服务端返回，前端不预测任务状态。
 */
 async function refresh() {
  const data = await Promise.all([
   api<Dashboard>('/dashboard'), api<Subscription[]>('/subscriptions'),
   api<Content[]>('/contents'), api<Settings>('/settings'),
   api<TaskGroup[]>('/task-groups'),
  ])
  dashboard.value = data[0]; subscriptions.value = data[1]
  contents.value = data[2]; settings.value = data[3]; taskGroups.value = data[4]
  if (!languageLoaded) { setLocale(data[3].ui.language); languageLoaded = true }
 }

 /**
 * 所有写操作具备可见的等待和失败状态。
 */
 async function mutate(action: () => Promise<unknown>, message = '已保存') {
  busy.value = true
  try {
   await action(); await refresh()
   toast.add({ severity: 'success', summary: t('操作成功'), detail: t(message), life: 3500 })
   return true
  }
  catch (e) { reportError(e); return false }
  finally { busy.value = false }
 }
 return { dashboard, subscriptions, taskGroups, contents, settings, busy, archiveMounted, archiveAvailable, subscriptionName, reportError, refresh, mutate }
})
