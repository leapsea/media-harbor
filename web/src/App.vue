<script setup lang="ts">
import { t } from './i18n'
import { computed, defineAsyncComponent, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { api, useMediaStore } from './stores/media'
import type { Subscription } from './types'
import Toast from 'primevue/toast'
import { formatDate } from './utils/formatDate'

/**
 * 页面按需装载复杂表单，首屏保持轻量。
 */
const SubscriptionEditor = defineAsyncComponent(() => import('./components/SubscriptionEditor.vue'))
const SystemSettings = defineAsyncComponent(() => import('./components/SystemSettings.vue'))
const DownloadHistory = defineAsyncComponent(() => import('./components/DownloadHistory.vue'))
const TaskManagement = defineAsyncComponent(() => import('./components/TaskManagement.vue'))
const store = useMediaStore()
const page = ref('overview')
const editing = ref(false)
const selected = ref<Subscription | null>(null)
const log = ref<string | null>(null)
const logSubscription = ref(0)
const logArea = ref<HTMLTextAreaElement | null>(null)
const loaded = ref(false)
let polling: ReturnType<typeof setInterval> | undefined
let refreshError = ''
const pages = [{ id: 'overview', label: '总览', icon: '◈' }, { id: 'tasks', label: '任务管理', icon: '≡' }, { id: 'history', label: '下载历史', icon: '◷' }, { id: 'settings', label: '系统设置', icon: '⚙' }]
const title = computed(() => pages.find(p => p.id === page.value)?.label)
const labels: Record<string, string> = { queued: '待执行', running: '运行中', completed: '已完成', failed: '失败', cancelled: '已取消', catchup: '补全下载', scan: '增量扫描', full: '全量初始化', download: '下载', mounted: '已挂载', unmounted: '未挂载', unknown: '未知' }

/** 页面切换回到顶部，让各个工作台从标题开始显示。 */
watch(page, () => window.scrollTo({ top: 0 }), { flush: 'post' })

function edit(subscription: Subscription | null) { selected.value = subscription; editing.value = true }
async function showLog(id: number) {
 const opening = log.value === null
 logSubscription.value = id
 try { log.value = (await api<{ log: string }>(`/subscriptions/${id}/log`)).log; if (opening) { await nextTick(); logArea.value?.focus() } }
 catch (e) { store.reportError(e) }
}

/** 日志弹窗内全选只作用于只读文本框，支持 Control 和 Command。 */
function selectLog(event: KeyboardEvent) {
 if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'a') {
  event.preventDefault()
  event.stopPropagation()
  logArea.value?.focus()
  logArea.value?.select()
 }
}

/** 同一轮持续断连只提示一次，恢复后再次失败仍会通知。 */
async function refreshDashboard() {
 try { await store.refresh(); loaded.value = true; refreshError = '' }
 catch (e) {
  const message = (e as Error).message
  if (message !== refreshError) store.reportError(e)
  refreshError = message
 }
}

/**
 * 轮询负责读状态，写操作等待状态由 Store 独立控制。
 */
onMounted(async () => {
 await refreshDashboard()
 polling = setInterval(async () => {
  if (store.busy) return
  await refreshDashboard()
  if (log.value !== null) await showLog(logSubscription.value)
 }, 4000)
})
onUnmounted(() => { if (polling) clearInterval(polling) })
</script>

<template>
 <div class="app-shell">
  <!-- 全局通知位于弹窗之上，不占用页面纵向空间。 -->
  <Toast position="top-right" :base-z-index="2000" :breakpoints="{ '700px': { width: 'calc(100% - 24px)', right: '12px', left: '12px' } }" :close-button-props="{ 'aria-label': t('关闭通知') }" />
  <aside class="sidebar">
   <a class="brand" href="#" @click.prevent="page = 'overview'"><span class="brand-icon">m<span>↓</span></span><span>Media<br><b>Harbor</b></span></a>
   <nav><button v-for="item in pages" :key="item.id" :class="{ selected: page === item.id }" @click="page = item.id"><span>{{ item.icon }}</span>{{ t(item.label) }}<i v-if="item.id === 'tasks' && store.dashboard?.counts.running" class="nav-dot"></i></button></nav>
   <div class="sidebar-footer"><span class="online-dot"></span>{{ t("单实例 · V1") }}</div>
  </aside>
  <main>
   <header class="page-header"><h1>{{ t(title) }}</h1><button class="primary" :disabled="!store.settings || store.busy" @click="edit(null)"><span>＋</span> {{ t("新增任务") }}</button></header>
   <p v-if="!loaded" class="empty">{{ t("正在加载工作台…") }}</p>
   <template v-if="page === 'overview' && store.dashboard">
    <!-- 概览优先呈现运行数据，统计不附加重复说明。 -->
    <div class="stats"><article><span>{{ t("任务总数") }}</span><strong>{{ store.dashboard.subscriptions }}</strong></article><article><span>{{ t("待执行 / 运行中") }}</span><strong>{{ store.dashboard.counts.queued }} <i>/</i> {{ store.dashboard.counts.running }}</strong></article><article><span>{{ t("已完成任务") }}</span><strong>{{ store.dashboard.counts.completed }}</strong></article><article><span>{{ t("失败任务") }}</span><strong :class="{ 'failure-number': store.dashboard.counts.failed }">{{ store.dashboard.counts.failed }}</strong></article></div>
    <section class="panel overview-panel"><div class="section-top"><h2>{{ t("最近下载") }}</h2><button @click="page = 'history'">{{ t("全部历史 →") }}</button></div><p v-if="!store.dashboard.recent.length" class="empty">{{ t("暂无下载记录") }}</p><div v-for="item in store.dashboard.recent" :key="item.id" class="media-row"><div class="media-icon">↓</div><div class="grow"><strong>{{ item.title || item.remote_id }}</strong><small>{{ store.subscriptionName(item.subscription_id) }} · {{ item.remote_id }}</small></div><span class="badge" :class="item.status">{{ t(labels[item.status]) }}</span></div></section>
    <!-- 目录按行展示，权限、挂载与检查时间可在同一屏比较。 -->
    <section class="panel overview-panel">
     <div class="section-top"><h2>{{ t("存储目录") }}</h2><div class="directory-actions"><span v-if="store.dashboard.directories[0]" class="help">UID {{ store.dashboard.directories[0].uid }} / GID {{ store.dashboard.directories[0].gid }}</span><button :disabled="store.busy" @click="store.mutate(() => api('/directories/check', 'POST'), t('目录检测已完成'))">{{ store.busy ? t("检测中…") : t("重新检测") }}</button></div></div>
     <div class="directory-list">
      <article v-for="dir in store.dashboard.directories" :key="dir.path" class="directory-row">
       <div class="directory-location"><h3>{{ t(dir.name) }}</h3><code :title="dir.path">{{ dir.path }}</code><p v-if="dir.error" class="error">{{ dir.error }}</p><p v-else-if="dir.name === '归档数据' && dir.mounted !== 'mounted'" class="directory-note">{{ t("归档暂不可用") }}</p></div>
       <div class="directory-state"><span class="badge" :class="dir.writable ? 'completed' : 'failed'">{{ dir.writable ? t("可读写") : t("不可用") }}</span><span class="mount-state">{{ t(labels[dir.mounted]) }}</span></div>
       <time class="directory-time" :datetime="dir.checked_at">{{ formatDate(dir.checked_at) }}</time>
      </article>
     </div>
    </section>
   </template>
   <TaskManagement v-if="page === 'tasks' && loaded" :labels="labels" @log="showLog" @edit="edit" />
   <DownloadHistory v-if="page === 'history' && loaded" :labels="labels" />
   <SystemSettings v-if="page === 'settings' && store.settings" :settings="store.settings" />
  </main>
  <SubscriptionEditor v-if="editing && store.settings" :subscription="selected" :settings="store.settings" :archive-available="Boolean(store.archiveAvailable)" @close="editing = false" />
  <div v-if="log !== null" class="modal-backdrop"><section class="modal log-modal" @keydown.capture="selectLog" role="dialog" aria-modal="true" aria-labelledby="log-title"><div class="section-top"><h2 id="log-title">{{ t('{name} · 任务日志', { name: store.subscriptionName(logSubscription) }) }}</h2><button class="icon-button" :aria-label="t('关闭日志')" @click="log = null">×</button></div><textarea ref="logArea" class="log-content" :aria-label="t('任务日志内容')" :value="log" readonly spellcheck="false"></textarea></section></div>
 </div>
</template>
