import { t, setLocale } from '../i18n'
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { useToast } from 'primevue/usetoast'
import type { Dashboard, Subscription, Content, ContentHistoryPage, Settings, TaskGroup } from '../types'
import type { Directory } from '../types'
import type { PageId } from '../utils/hashRoute'
import { confirmMutation } from '../utils/mutation'

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
 * 状态与操作分离：刷新只读，mutate 确认写入后在后台同步展示数据。
 */
export const useMediaStore = defineStore('media', () => {
 const toast = useToast()
 const dashboard = ref<Dashboard | null>(null)
 const subscriptions = ref<Subscription[]>([])
 const taskGroups = ref<TaskGroup[]>([])
 const contents = ref<Content[]>([])
 const contentsLoaded = ref(false)
 const historyTotal = ref(0)
 const historyPage = ref(1)
 const historyPageSize = ref(20)
 let historyPageSizeSelected = false
 const directories = ref<Directory[]>([])
 const settings = ref<Settings | null>(null)
 const busy = ref(false)
 const refreshing = ref(false)
 let activePage: PageId = 'overview'
 let currentRefresh: Promise<void> | null = null
 let queuedPage: PageId | null = null
 /** 归档入口和任务表单共享当前目录状态，避免页面各自判断。 */
 const archiveMounted = computed(() => directories.value[2]?.mounted === 'mounted')
 const archiveAvailable = computed(() => archiveMounted.value && directories.value[2]?.writable)
 /** 记录显示名统一由任务配置提供，配置已删除时保留任务 ID。 */
 function subscriptionName(id: number) { return subscriptions.value.find(s => s.id === id)?.display_name || t('任务 #{id}', { id }) }

 /**
 * 请求失败统一以浮动通知呈现，弹窗和页面共享同一个 Toast 宿主。
 */
 function reportError(error: unknown) {
  toast.add({ severity: 'error', summary: t('操作失败'), detail: t(error instanceof Error ? error.message : String(error)), life: 8000 })
 }

/**
 * 每个页面仅读取自身展示所需的数据；串行化刷新以避免慢请求重叠。
 */
async function loadPage(page: PageId) {
  if (page === 'overview') {
   const data = await api<Dashboard>('/dashboard')
   dashboard.value = data
   directories.value = data.directories
   setLocale(data.language)
  } else if (page === 'tasks') {
   const [nextSubscriptions, nextGroups, nextSettings, nextDirectories] = await Promise.all([
    api<Subscription[]>('/subscriptions'), api<TaskGroup[]>('/task-groups'),
    api<Settings>('/settings'), api<Directory[]>('/directories'),
   ])
   subscriptions.value = nextSubscriptions
   taskGroups.value = nextGroups
   settings.value = nextSettings
   directories.value = nextDirectories
   setLocale(nextSettings.ui.language)
  } else if (page === 'history') {
   if (!contentsLoaded.value) {
    const [nextHistory, nextSubscriptions, nextSettings, nextDirectories] = await Promise.all([
     api<ContentHistoryPage>(`/contents/page?page=${historyPage.value}`), api<Subscription[]>('/subscriptions'),
     api<Settings>('/settings'), api<Directory[]>('/directories'),
    ])
    subscriptions.value = nextSubscriptions
    settings.value = nextSettings
    directories.value = nextDirectories
    setLocale(nextSettings.ui.language)
    applyHistoryPage(nextHistory)
   } else {
    const sizeQuery = historyPageSizeSelected ? `&size=${historyPageSize.value}` : ''
    const nextHistory = await api<ContentHistoryPage>(`/contents/page?page=${historyPage.value}${sizeQuery}`)
    applyHistoryPage(nextHistory)
   }
  } else {
   const [nextSettings, nextDirectories] = await Promise.all([
    api<Settings>('/settings'), api<Directory[]>('/directories'),
   ])
   settings.value = nextSettings
   directories.value = nextDirectories
   setLocale(nextSettings.ui.language)
  }
}

function applyHistoryPage(data: ContentHistoryPage) {
 contents.value = data.items
 historyTotal.value = data.total
 historyPage.value = data.page
 historyPageSize.value = data.page_size
 contentsLoaded.value = true
}

function setHistoryPagination(page: number, size: number) {
 historyPage.value = page
 historyPageSize.value = size
 historyPageSizeSelected = true
 void refresh('history').catch(reportError)
}

function setActivePage(page: PageId) {
 activePage = page
 if (page !== 'history') historyPageSizeSelected = false
}

function refresh(page: PageId = activePage) {
 activePage = page
 if (currentRefresh) {
  queuedPage = page
  return currentRefresh
 }
 const run = async () => {
  refreshing.value = true
  let target = page
  try {
   while (true) {
    queuedPage = null
    try { await loadPage(target) }
    catch (error) {
     if (queuedPage === null) throw error
    }
    if (queuedPage === null) break
    target = queuedPage
   }
  } finally {
   refreshing.value = false
   currentRefresh = null
  }
 }
 currentRefresh = run()
 return currentRefresh
 }

 /**
 * 等待状态只覆盖写请求；成功提示与页面刷新解耦，避免慢查询阻塞操作确认。
 */
 async function mutate(action: () => Promise<unknown>, message = '已保存') {
  busy.value = true
  try {
   return await confirmMutation(action, () => refresh(),
    () => toast.add({ severity: 'success', summary: t('操作成功'), detail: t(message), life: 3500 }),
    reportError,
    error => toast.add({ severity: 'error', summary: t('页面刷新失败'), detail: t(error instanceof Error ? error.message : String(error)), life: 8000 }))
  }
  catch (e) { reportError(e); return false }
  finally { busy.value = false }
 }
 return { dashboard, subscriptions, taskGroups, contents, contentsLoaded, historyTotal, historyPage, historyPageSize, directories, settings, busy, refreshing, archiveMounted, archiveAvailable, subscriptionName, reportError, setActivePage, setHistoryPagination, refresh, mutate }
})
