<script setup lang="ts">
import { t } from '../i18n'
import { ref } from 'vue'
import { useToast } from 'primevue/usetoast'
import type { Content } from '../types'
import { api, useMediaStore } from '../stores/media'
import ListPagination from './ListPagination.vue'
import { artifactName, artifactDirectories } from '../utils/artifactName'
import { formatDate } from '../utils/formatDate'

/** 历史记录与分页独立管理，应用入口只负责页面切换。 */
const props = defineProps<{ labels: Record<string, string> }>()
const store = useMediaStore()
const toast = useToast()
const copying = ref<number | null>(null)
const artifactLabels: Record<string, string> = { video: '视频', audio: '音频', subtitles: '字幕', comments: '评论', thumbnail: '缩略图', metadata: '元数据', image: '图片' }
function changePage(page: number) { store.setHistoryPagination(page, store.historyPageSize) }
function changeSize(size: number) { store.setHistoryPagination(1, size) }
/** 复制当前真实产物目录，归档后使用新的位置，不刷新下载状态。 */
async function copyDirectory(item: Content) {
 copying.value = item.id
 try {
  await navigator.clipboard.writeText(artifactDirectories(item.files, item.paths).join('\n'))
  toast.add({ severity: 'success', summary: t('已复制'), detail: t('文件夹路径已复制'), life: 3500 })
 } catch (error) { store.reportError(error) }
 finally { copying.value = null }
}
</script>

<template>
 <section class="panel history-list" :aria-label="t('下载记录')">
  <!-- 清空只调用历史接口，任务队列和文件操作保持各自职责。 -->
  <div class="history-actions"><button class="danger" :disabled="store.busy || !store.contentsLoaded || !store.contents.length" @click="store.mutate(() => api('/contents/history', 'DELETE'), t('下载历史已清空'))">{{ t("清空历史记录") }}</button></div>
  <p v-if="!store.contentsLoaded" class="empty">{{ t("正在加载下载记录…") }}</p>
  <p v-else-if="!store.contents.length" class="empty">{{ t("暂无下载记录") }}</p>
  <article v-for="item in store.contents" :key="item.id" class="history-card">
   <div class="section-top">
    <div><h3>{{ item.title || item.remote_id }}</h3><p class="help">{{ t('{author} · 发布于 {time}', { author: store.subscriptionName(item.subscription_id), time: formatDate(item.published) }) }}</p></div>
    <span class="badge" :class="item.status">{{ t(props.labels[item.status]) }}</span>
   </div>
   <!-- 标签直接贴在对应产物旁，info JSON 同时标记评论和元数据。 -->
   <div class="history-files">
    <div v-for="file in item.files" :key="file.path" class="history-file">
     <span class="file-tags"><span v-for="tag in file.tags" :key="tag" class="badge">{{ t(artifactLabels[tag] || tag) }}<template v-if="tag === 'comments' && file.comment_count !== undefined"> · {{ file.comment_count }} {{ t("条") }}</template></span><span v-if="!file.tags?.length" class="badge">{{ t("文件") }}</span></span>
     <div class="file-description"><code :title="file.path">{{ artifactName(file.path, item.remote_id, item.title) }}</code>
     </div>
    </div>
   </div>
   <p v-if="item.error" class="error">{{ item.error }}</p>
   <div class="section-top"><span class="help">{{ item.archived ? t("已移入归档目录") : t("尚未归档") }}</span><div class="actions">
    <button v-if="item.paths?.length" :disabled="copying !== null" @click="copyDirectory(item)">{{ copying === item.id ? t("正在复制…") : t("复制文件夹路径") }}</button>
    <button v-if="item.status === 'completed' && !item.archived" :disabled="store.busy || !store.archiveAvailable" @click="store.mutate(() => api(`/contents/${item.id}/archive`, 'POST'), t('归档已完成'))">{{ t("归档 / 重试归档") }}</button></div></div>
  </article>
  <ListPagination :page="store.historyPage" :size="store.historyPageSize" :total="store.historyTotal" @update:page="changePage" @update:size="changeSize" />
 </section>
</template>
