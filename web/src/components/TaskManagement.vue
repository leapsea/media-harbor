<script setup lang="ts">
import { t } from '../i18n'
import { computed } from 'vue'
import { api, useMediaStore } from '../stores/media'
import type { Subscription } from '../types'
import { taskMode } from '../utils/taskMode'
import { parseSchedule } from '../utils/schedule'
import { usePagination } from '../composables/usePagination'
import ListPagination from './ListPagination.vue'
import { formatDate } from '../utils/formatDate'

/** 管理配置和执行汇总共用一行，未启动的主页也显示在列表中。 */
const props = defineProps<{ labels: Record<string, string> }>()
const emit = defineEmits<{ log: [id: number]; edit: [task: Subscription] }>()
const store = useMediaStore()
const { page, size, items } = usePagination(() => store.subscriptions, () => store.settings!.ui.task_page_size)
/** 轮询后只构造一次索引，模板中多次读取状态不反复遍历队列。 */
const groups = computed(() => new Map(store.taskGroups.map(item => [item.subscription_id, item])))
const group = (id: number) => groups.value.get(id)
const isContent = (task: Subscription) => taskMode(task.platform, task.url) === 'content'
/** 周期以可读文案展示，首次显式启动前不触发定时扫描。 */
function schedule(task: Subscription) {
 if (isContent(task)) return t('立即执行 · 单次')
 if (!task.schedule) return t('仅手动执行')
 const value = parseSchedule(task.schedule)
 if (value.mode === 'interval') return t('每 {count} {unit}', { count: value.interval, unit: t(value.unit === 'h' ? '小时' : '分钟') })
 if (value.mode === 'daily') return t('每天 {time}', { time: `${value.hour}:${String(value.minute).padStart(2, '0')}` })
 if (value.mode === 'weekly') return t('每周 {day} {time}', { day: t(['周日', '周一', '周二', '周三', '周四', '周五', '周六'][value.weekday]), time: `${value.hour}:${String(value.minute).padStart(2, '0')}` })
 return task.schedule
}
/** 所有执行入口复用原调度接口，失败重试不会扩大测试批次。 */
async function scan(id: number, kind: string) {
 await store.mutate(() => api(`/subscriptions/${id}/scan?kind=${kind}`, 'POST'), '任务已加入队列')
}
async function action(id: number, kind: 'cancel' | 'retry') {
 await store.mutate(() => api(`/subscriptions/${id}/tasks/${kind}`, 'POST'), kind === 'cancel' ? '待执行与运行任务已取消' : '失败任务已重新排队')
}
</script>

<template>
 <section class="panel" :aria-label="t('任务列表')">
  <!-- 页面标题由应用入口提供，总数量由分页控件统一展示。 -->
  <p v-if="!store.subscriptions.length" class="empty">{{ t("暂无任务") }}</p>
  <!-- 配置、状态和下载汇总在同一行；窄屏只在表格内部横向滚动。 -->
  <div v-else class="table-wrap"><table class="task-table management-table"><thead><tr><th>{{ t("任务") }}</th><th>{{ t("平台 / 执行方式") }}</th><th>{{ t("任务状态") }}</th><th>{{ t("下载进度") }}</th><th>{{ t("最近更新") }}</th><th>{{ t("操作") }}</th></tr></thead>
   <tbody><tr v-for="task in items" :key="task.id">
    <td class="task-name"><div class="task-author"><img v-if="task.avatar_url" class="task-avatar" :src="task.avatar_url" alt="" loading="lazy"><div class="task-author-text"><strong>{{ task.display_name }}</strong><a :href="task.url" target="_blank" rel="noreferrer" :title="task.url">{{ task.url }}</a></div></div></td>
    <td><span class="badge">{{ task.platform === 'youtube' ? 'YouTube' : 'Instagram' }}</span><small class="help">{{ schedule(task) }}</small><small v-if="!isContent(task)" class="help">{{ !group(task.id) ? t("首次启动后启用周期") : task.enabled ? t('下次 {time}', { time: formatDate(task.next_run) }) : t("定时已暂停") }}</small></td>
    <td><span class="badge" :class="group(task.id)?.status">{{ t(props.labels[group(task.id)?.status || ''] || t("未开始")) }}</span><small v-if="group(task.id)?.scan_status" class="help">{{ isContent(task) ? t("内容解析") : t("扫描") }} · {{ t(props.labels[group(task.id)!.scan_status]) }}</small></td>
    <td><strong>{{ group(task.id)?.counts.completed || 0 }} / {{ group(task.id)?.total || 0 }}</strong><small class="help">{{ t('待执行 {queued} · 运行 {running} · 失败 {failed}', { queued: group(task.id)?.counts.queued || 0, running: group(task.id)?.counts.running || 0, failed: group(task.id)?.counts.failed || 0 }) }}</small></td>
    <td>{{ formatDate(group(task.id)?.updated_at || task.created_at) }}</td>
    <td><div class="actions">
     <template v-if="!isContent(task)"><button :disabled="store.busy" @click="scan(task.id, 'full')">{{ t("全量初始化") }}</button><button :disabled="store.busy" @click="scan(task.id, 'scan')">{{ t("增量扫描") }}</button><button :disabled="store.busy" @click="scan(task.id, 'catchup')">{{ t("补全下载") }}</button></template>
     <button @click="emit('log', task.id)">{{ t("日志") }}</button>
     <button v-if="['running', 'queued'].includes(group(task.id)?.status || '')" :disabled="store.busy" @click="action(task.id, 'cancel')">{{ t("取消") }}</button>
     <button v-if="group(task.id)?.counts.failed || group(task.id)?.counts.cancelled || ['failed', 'cancelled'].includes(group(task.id)?.scan_status || '')" :disabled="store.busy" @click="action(task.id, 'retry')">{{ t("重试") }}</button>
     <button @click="emit('edit', task)">{{ t("编辑") }}</button>
     <button v-if="!isContent(task) && task.schedule" :disabled="store.busy" @click="store.mutate(() => api(`/subscriptions/${task.id}`, 'PUT', { ...task, enabled: !task.enabled }), task.enabled ? t('定时已暂停') : t('定时已恢复'))">{{ task.enabled ? t("暂停定时") : t("恢复定时") }}</button>
     <button class="danger" :disabled="store.busy" @click="store.mutate(() => api(`/subscriptions/${task.id}`, 'DELETE'), t('任务已删除，文件继续保留'))">{{ t("删除") }}</button>
    </div></td>
   </tr></tbody>
  </table></div>
  <ListPagination v-model:page="page" v-model:size="size" :total="store.subscriptions.length" />
 </section>
</template>
