import { t, setLocale } from '../i18n'
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { useToast } from 'primevue/usetoast'
import type { Dashboard, Subscription, Content, ContentHistoryPage, Settings, TaskGroup } from '../types'
import type { Directory } from '../types'
import type { PageId } from '../utils/hashRoute'
import { confirmMutation } from '../utils/mutation'
import { createRequestPool } from '../utils/requestPool'

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
 const pageLoading = ref<Record<PageId, boolean>>({ overview: false, tasks: false, history: false, settings: false })
 const refreshing = computed(() => pageLoading.value[activePage.value])
 const subscriptionsLoaded = ref(false)
 const taskGroupsLoaded = ref(false)
 const requests = createRequestPool()
 const pageRequests = createRequestPool()
 let historyRevision = 0
 const activePage = ref<PageId>('overview')
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

/** 各资源返回后立即提交，慢接口不遮住已经可展示的页面内容。 */
function loadSettings() {
 return requests.run('settings', async () => {
  const data = await api<Settings>('/settings')
  settings.value = data
  setLocale(data.ui.language)
 })
}
function loadDirectories() {
 return requests.run('directories', async () => { directories.value = await api<Directory[]>('/directories') })
}
function loadSubscriptions() {
 return requests.run('subscriptions', async () => {
  subscriptions.value = await api<Subscription[]>('/subscriptions')
  subscriptionsLoaded.value = true
 })
}
/** 分页立即响应点击；先前页的慢响应不能将当前页和记录回退。 */
async function loadHistory() {
 const revision = ++historyRevision
 const sizeQuery = historyPageSizeSelected ? `&size=${historyPageSize.value}` : ''
 const path = `/contents/page?page=${historyPage.value}${sizeQuery}`
 const data = await requests.run(path, () => api<ContentHistoryPage>(path))
 if (revision === historyRevision) applyHistoryPage(data)
}
async function loadPage(page: PageId) {
 let loads: Promise<unknown>[]
 if (page === 'overview') {
  loads = [requests.run('dashboard', async () => {
   const data = await api<Dashboard>('/dashboard')
   dashboard.value = data
   directories.value = data.directories
   setLocale(data.language)
  })]
 } else if (page === 'tasks') {
  loads = [loadSubscriptions(), requests.run('task-groups', async () => {
   taskGroups.value = await api<TaskGroup[]>('/task-groups')
   taskGroupsLoaded.value = true
  }), loadSettings(), loadDirectories()]
 } else if (page === 'history') {
  loads = [loadHistory()]
  if (!subscriptionsLoaded.value) loads.push(loadSubscriptions())
  if (!settings.value) loads.push(loadSettings())
  if (!directories.value.length) loads.push(loadDirectories())
 } else {
  loads = [loadSettings(), loadDirectories()]
 }
 // 等待仅用于本页加载状态和错误收口，不控制组件挂载或其他页面的请求。
 const results = await Promise.allSettled(loads)
 const failure = results.find(result => result.status === 'rejected')
 if (failure?.status === 'rejected') throw failure.reason
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
 void loadHistory().catch(reportError)
}

function setActivePage(page: PageId) {
 activePage.value = page
 if (page !== 'history') historyPageSizeSelected = false
}

/** 页面之间并行，同一页轮询复用进行中的请求，导航无需等待旧页。 */
function refresh(page: PageId = activePage.value) {
 return pageRequests.run(page, async () => {
  pageLoading.value[page] = true
  try { await loadPage(page) }
  finally { pageLoading.value[page] = false }
 })
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
 return { dashboard, subscriptions, taskGroups, contents, contentsLoaded, historyTotal, historyPage, historyPageSize, directories, settings, busy, refreshing, pageLoading, subscriptionsLoaded, taskGroupsLoaded, archiveMounted, archiveAvailable, subscriptionName, reportError, setActivePage, setHistoryPagination, refresh, mutate }
})
