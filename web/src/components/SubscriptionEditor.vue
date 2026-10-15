<script setup lang="ts">
import { t } from '../i18n'
import { computed, onUnmounted, ref, toRaw, watch } from 'vue'
import { api, useMediaStore } from '../stores/media'
import type { Subscription, Settings } from '../types'
import YouTubeOptions from './YouTubeOptions.vue'
import SchedulePicker from './SchedulePicker.vue'
import { taskMode } from '../utils/taskMode'
import { directoryNameFromUrl } from '../utils/directoryName'
import { useDryRun } from '../composables/useDryRun'
import DryRunLog from './DryRunLog.vue'

/**
 * 编辑使用独立副本，取消不会影响 Dashboard 当前状态。
 */
const props = defineProps<{ subscription: Subscription | null; settings: Settings; archiveAvailable: boolean }>()
const emit = defineEmits<{ close: [] }>()
const store = useMediaStore()
const dryrun = useDryRun()
const form = ref<Subscription>(props.subscription ? structuredClone(toRaw(props.subscription)) : {
 id: 0, platform: 'youtube', url: '', mode: 'profile', stable_id: '', display_name: '', current_name: '',
 enabled: true, schedule: '@every 6h', template: '',
 archive: props.archiveAvailable && props.settings.auto_archive, youtube: null,
 last_scan: '', last_success: '', next_run: '', created_at: '',
})
const override = ref(form.value.youtube !== null)
const options = ref(structuredClone(toRaw(form.value.youtube || props.settings.youtube)))
/** 目录状态晚到时补入真实归档默认值，用户已选过的值继续保留。 */
const archiveTouched = ref(false)
watch(() => props.archiveAvailable, available => {
 if (!form.value.id && !archiveTouched.value) form.value.archive = available && props.settings.auto_archive
})
const mode = computed(() => taskMode(form.value.platform, form.value.url))
/** 地址切换立即调整执行方式；主页保持按间隔，详情固定单次。 */
watch(mode, value => {
 form.value.mode = value
 form.value.schedule = value === 'content' ? '' : '@every 6h'
 form.value.enabled = value !== 'content'
})
const customName = ref(false)
const nameLoading = ref(false)
let nameTimer: ReturnType<typeof setTimeout> | undefined
let nameRevision = 0
let resolvedKey = ''
let nameRequest: Promise<void> | undefined

/**
 * 新建时自动预填名称；用户手动改名后保留输入，编辑已有订阅不改变固定目录。
 */
async function suggestName() {
 if (form.value.id || customName.value) return
 const url = form.value.url.trim()
 let parsed: URL
 try { parsed = new URL(url) } catch { form.value.display_name = ''; return }
 if (!['http:', 'https:'].includes(parsed.protocol)) return
 const key = `${form.value.platform}:${url}`
 if (key === resolvedKey) return nameRequest
 resolvedKey = key
 const revision = ++nameRevision
 form.value.display_name = directoryNameFromUrl(url)
 nameLoading.value = true
 /** 异步响应只更新当前地址，手动填写的名称不会被覆盖。 */
 nameRequest = (async () => {
  try {
   const result = await api<{ name: string }>('/subscriptions/resolve-name', 'POST', { platform: form.value.platform, url })
   if (revision === nameRevision && !customName.value) form.value.display_name = result.name
  } catch (error) {
   if (revision === nameRevision && !customName.value) store.reportError(error)
  } finally {
   if (revision === nameRevision) nameLoading.value = false
  }
 })()
 return nameRequest
}
watch(() => [form.value.url, form.value.platform], () => {
 if (nameTimer) clearTimeout(nameTimer)
 ++nameRevision
 resolvedKey = ''
 nameLoading.value = false
 if (!form.value.id && !customName.value) form.value.display_name = directoryNameFromUrl(form.value.url)
 nameTimer = setTimeout(() => { void suggestName() }, 400)
})
onUnmounted(() => { ++nameRevision; if (nameTimer) clearTimeout(nameTimer) })

/**
 * 主页只保存配置；详情首次创建由服务端排入单次执行。
 */
async function save() {
 if (nameTimer) clearTimeout(nameTimer)
 await suggestName()
 if (!form.value.display_name.trim()) return
 form.value.youtube = override.value ? options.value : null
 const ok = await store.mutate(async () => {
  /**
 * 时间和身份字段由后端维护，新建表单不发送空时间字符串。
 */
  const { last_scan, last_success, next_run, created_at, ...payload } = form.value
  await api<Subscription>(`/subscriptions${form.value.id ? `/${form.value.id}` : ''}`, form.value.id ? 'PUT' : 'POST', payload)

 }, form.value.id ? '任务已保存' : mode.value === 'content' ? '任务已创建，立即执行' : '任务已创建，等待手动启动')
 if (ok) emit('close')
}

/** 试运行使用当前未保存的表单选项，不触发名称保存、任务创建或队列执行。 */
function dryRun() {
 if (nameTimer) clearTimeout(nameTimer)
 try { const address = new URL(form.value.url); if (!['http:', 'https:'].includes(address.protocol)) throw new Error() }
 catch { store.reportError(new Error(t('请输入有效任务 URL'))); return }
 const { last_scan, last_success, next_run, created_at, ...payload } = form.value
 void dryrun.start({ ...payload, youtube: override.value ? options.value : null })
}
</script>

<template>
 <div class="modal-backdrop">
  <section class="modal subscription-modal" role="dialog" aria-modal="true" aria-labelledby="editor-title">
   <div class="section-top"><h2 id="editor-title">{{ form.id ? t("编辑任务") : t("新增任务") }}</h2><button class="icon-button" :aria-label="t('关闭')" @click="emit('close')">×</button></div>
   <form @submit.prevent="save">
    <div class="form-grid subscription-fields">
     <!-- 只有两个平台，直接点击选择并显示选中状态，减少展开下拉框的步骤。 -->
     <div class="form-row platform-picker" role="group" aria-labelledby="platform-label"><span id="platform-label">{{ t("平台选择") }}</span>
      <div class="platform-choices">
       <label :class="{ chosen: form.platform === 'youtube' }"><input v-model="form.platform" type="radio" value="youtube" :disabled="Boolean(form.id)"><span class="platform-symbol youtube-symbol" aria-hidden="true">▶</span>YouTube<span class="platform-check" aria-hidden="true">✓</span></label>
       <label :class="{ chosen: form.platform === 'instagram' }"><input v-model="form.platform" type="radio" value="instagram" :disabled="Boolean(form.id)"><span class="platform-symbol instagram-symbol" aria-hidden="true">◎</span>Instagram<span class="platform-check" aria-hidden="true">✓</span></label>
      </div>
     </div>
     <label>{{ t("任务地址") }}<input v-model="form.url" type="url" required :placeholder="form.platform === 'youtube' ? t('YouTube 频道、播放列表或视频链接') : t('Instagram 主页、帖子或 Reel 链接')"></label>
     <label>{{ t("目录名称") }}<input v-model="form.display_name" :disabled="Boolean(form.id)" :placeholder="nameLoading ? t('正在解析作者名称…') : t('作者名称')" required @input="customName = Boolean(form.display_name)"></label>
     <SchedulePicker v-model="form.schedule" :immediate="mode === 'content'" />
     <label>{{ t("自动归档") }}<select v-model="form.archive" :disabled="!archiveAvailable && !form.archive" @change="archiveTouched = true"><option :value="false">{{ archiveAvailable ? t("不归档 · 保留在下载目录") : t("归档不可用 · 未挂载或不可写") }}</option><option :value="true" :disabled="!archiveAvailable">{{ t("归档 · 移入归档目录") }}</option></select></label>
     <label>{{ t("目录模板") }}<input v-model="form.template" :placeholder="settings.template"></label>
    </div>
    <!-- 次要选项可展开查看，主要字段保持用户指定的顺序。 -->
    <details v-if="form.platform === 'youtube' || (mode === 'profile' && form.schedule)" class="other-settings" :open="override">
     <summary>{{ t("其他设置") }}</summary>
    <div v-if="mode === 'profile' && form.schedule" class="options-grid">
     <label class="check"><input v-model="form.enabled" type="checkbox">{{ t("启用定时扫描") }}</label>
    </div>
    <template v-if="form.platform === 'youtube'">
     <label class="check"><input v-model="override" type="checkbox">{{ t("自定义 YouTube 下载选项") }}</label>
     <YouTubeOptions v-if="override" v-model="options" :defaults="settings.youtube" />
    </template>
    </details>
    <footer class="form-actions"><button type="button" @click="emit('close')">{{ t("取消") }}</button><button v-if="!form.id" type="button" :disabled="store.busy || nameLoading || dryrun.running.value || !form.url.trim()" @click="dryRun">{{ dryrun.running.value ? t('验证中…') : 'Dryrun' }}</button><button class="primary" :disabled="store.busy || nameLoading || dryrun.running.value">{{ nameLoading ? t("正在解析作者…") : store.busy ? t("正在保存…") : t("保存任务") }}</button></footer>
   </form>
  </section>
 </div>
 <DryRunLog v-if="dryrun.visible.value" :log="dryrun.log.value" :status="dryrun.status.value" :running="dryrun.running.value" @close="dryrun.close" />
</template>
