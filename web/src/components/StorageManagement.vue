<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api, useMediaStore } from '../stores/media'
import { t } from '../i18n'

/** 存储占用按需刷新，独立于可编辑的系统设置表单。 */
interface Usage { path: string; bytes: number; files: number; cleanable_bytes: number; cleanable_directories: number; retained_directories: number }
const storage = ref<{ logs: Usage; work: Usage } | null>(null)
const loading = ref(false)
const store = useMediaStore()
async function refresh() {
 loading.value = true
 try { storage.value = await api('/storage') }
 catch (error) { store.reportError(error) }
 finally { loading.value = false }
}
/** 清理后重新读取真实占用，运行日志可能继续增长。 */
async function clean(kind: 'logs' | 'work') {
 if (await store.mutate(() => api(`/storage/${kind}`, 'DELETE'), kind === 'logs' ? '日志已清空' : '无用缓存已清理')) await refresh()
}
function size(bytes: number) {
 const units = ['B', 'KiB', 'MiB', 'GiB']
 let index = 0
 while (bytes >= 1024 && index < units.length - 1) { bytes /= 1024; index++ }
 return `${index ? bytes.toFixed(1) : bytes} ${units[index]}`
}
onMounted(refresh)
</script>
<template>
 <section class="panel settings-panel storage-panel" :aria-label="t('存储管理')">
  <div class="settings-engine-actions"><button :disabled="loading || store.busy" @click="refresh">{{ loading ? t('正在刷新…') : t('刷新占用') }}</button></div>
  <template v-if="storage">
   <div class="storage-row">
    <strong>{{ t('任务日志') }}</strong><code class="storage-path">{{ storage.logs.path }}</code>
    <div class="storage-metrics"><span>{{ size(storage.logs.bytes) }}</span><span>{{ t('{count} 个文件', { count: storage.logs.files }) }}</span></div>
    <button class="danger" :disabled="store.busy || loading || !storage.logs.files" @click="clean('logs')">{{ t('清空日志') }}</button>
   </div>
   <div class="storage-row">
    <strong>{{ t('工作缓存') }}</strong><code class="storage-path">{{ storage.work.path }}</code>
    <div class="storage-metrics"><span>{{ size(storage.work.bytes) }}</span><span>{{ t('{count} 个文件', { count: storage.work.files }) }}</span></div>
    <button :disabled="store.busy || loading || !storage.work.cleanable_directories" @click="clean('work')">{{ t('清理无用缓存') }}</button>
    <div class="storage-work-counts"><span>{{ t('可清理：{count} 项', { count: storage.work.cleanable_directories }) }} · {{ size(storage.work.cleanable_bytes) }}</span><span>{{ t('续接 / 重试保留：{count} 项', { count: storage.work.retained_directories }) }}</span></div>
   </div>
  </template>
 </section>
</template>
