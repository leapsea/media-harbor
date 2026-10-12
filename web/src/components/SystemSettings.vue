<script setup lang="ts">
import { t, locale } from '../i18n'
import { onMounted, ref, toRaw, watch } from 'vue'
import { api, useMediaStore } from '../stores/media'
import type { Settings } from '../types'
import YouTubeOptions from './YouTubeOptions.vue'
import UISettingsOptions from './UISettingsOptions.vue'
import StorageManagement from './StorageManagement.vue'
import ToggleSwitch from 'primevue/toggleswitch'
/**
 * 系统设置以本地表单副本编辑，后台轮询不会覆盖尚未保存的输入。
 */
const props = defineProps<{ settings: Settings }>()
const store = useMediaStore()
const form = ref(structuredClone(toRaw(props.settings)))
form.value.ui.language = locale.value
/** 挂载丢失时取消默认归档；新挂载时勾选，已有挂载下保留用户的选择。 */
watch(() => store.archiveMounted, (mounted, previous) => {
 if (!mounted) form.value.auto_archive = false
 else if (previous === false) form.value.auto_archive = true
}, { immediate: true })
/** 总开关沿用后台的 0（不限）语义，关闭时输入框保留当前编辑值。 */
const debugEnabled = ref(form.value.debug.test_item_limit > 0)
const debugLimit = ref(String(form.value.debug.test_item_limit || 10))
/** 过滤键入及粘贴中的非数字字符，避免科学计数法、小数或负数。 */
function setDebugLimit(event: Event) {
 const input = event.target as HTMLInputElement
 input.value = input.value.replace(/[^0-9]/g, '')
 debugLimit.value = input.value
}
/** 总开关关闭时保存不限；其他分类保存也使用同一份调试配置。 */
async function saveSettings() {
 const limit = Number(debugLimit.value)
 // 切换分类后也校验编辑值，避免空值或超出整数精度的值被保存为关闭状态。
 if (debugEnabled.value && (!Number.isSafeInteger(limit) || limit < 1)) {
  store.reportError(new Error(t('测试条目限制必须为正整数')))
  return
 }
 await store.mutate(() => api('/settings', 'PUT', {
  ...form.value, debug: { test_item_limit: debugEnabled.value ? limit : 0 },
 }))
}
/** 分类切换只控制展示，本地表单副本持续保留编辑内容。 */
const category = ref('display')
const categories = [
 { id: 'display', label: '界面' }, { id: 'files', label: '文件与归档' },
 { id: 'youtube', label: '下载选项' }, { id: 'scheduling', label: '并发调度' },
 { id: 'cookies', label: 'Cookie' }, { id: 'engines', label: '引擎' },
 { id: 'storage', label: '存储' }, { id: 'debug', label: '调试' },
]
const galleryConfig = ref('{}')
const cookies = ref<Record<string, boolean>>({ youtube: false, instagram: false })
const cookiePlatform = ref<string | null>(null)
const cookieText = ref('')
const versions = ref<Record<string, string>>({})
/**
 * 引擎版本按需读取，避免 Dashboard 轮询反复创建进程。
 */
async function loadVersions() {
 const result = await api<{ versions: Record<string, string> }>('/engines')
 versions.value = result.versions
}
onMounted(async () => {
 try {
  galleryConfig.value = (await api<{ config: string }>('/gallery-config')).config
  for (const p of ['youtube', 'instagram']) cookies.value[p] = (await api<{ configured: boolean }>(`/cookies/${p}`)).configured
  await loadVersions()
 } catch (e) { store.reportError(e) }
})
/** 点击编辑时读取配置，关闭弹窗后清空编辑副本。 */
async function editCookie(platform: string) {
 try {
  cookieText.value = (await api<{ content: string }>(`/cookies/${platform}/content`)).content
  cookiePlatform.value = platform
 } catch (e) { store.reportError(e) }
}
function closeCookie() { cookiePlatform.value = null; cookieText.value = '' }
/** 粘贴的 Netscape 文本以 JSON 提交，状态标签仅用于显示。 */
async function saveCookie() {
 const platform = cookiePlatform.value
 if (!platform) return
 if (await store.mutate(() => api(`/cookies/${platform}`, 'PUT', { content: cookieText.value }), 'Cookie 已保存')) {
  cookies.value[platform] = true; closeCookie()
 }
}
async function removeCookie(platform: string) {
 if (await store.mutate(() => api(`/cookies/${platform}`, 'DELETE'), 'Cookie 已删除')) cookies.value[platform] = false
}
</script>
<template>
 <nav class="settings-categories" :aria-label="t('设置分类')">
  <button v-for="item in categories" :key="item.id" :aria-pressed="category === item.id" :class="{ selected: category === item.id }" @click="category = item.id">{{ t(item.label) }}</button>
 </nav>
 <!-- 分类导航已标明内容，卡片不重复显示分类标题。 -->
 <!-- 通用设置共用保存接口；分类切换不销毁控件或覆盖编辑副本。 -->
 <form v-show="!['cookies', 'engines', 'storage'].includes(category)" class="settings-form" @submit.prevent="saveSettings">
  <section v-show="category === 'display'" class="panel settings-panel" :aria-label="t('界面设置')">
   <UISettingsOptions v-model="form.ui" />
  </section>
  <section v-show="category === 'files'" class="panel settings-panel" :aria-label="t('文件与归档设置')">
   <div class="settings-group"><h3>{{ t("目录规则") }}</h3>
   <div class="form-grid"><label class="wide">{{ t("默认目录模板") }}<input v-model="form.template" required></label></div>
   <label class="check"><input v-model="form.use_upload_date" type="checkbox">{{ t("按 UP 主的上传 / 发布时间创建日期目录") }}</label>
   </div>
   <div class="settings-group"><h3>{{ t("自动归档") }}</h3>
   <label class="check"><input v-model="form.auto_archive" type="checkbox" :disabled="!store.archiveMounted">{{ t("新任务默认开启自动归档") }}</label></div>
  </section>
  <section v-show="category === 'youtube'" class="panel settings-panel" :aria-label="t('YouTube 默认下载设置')">
   <YouTubeOptions v-model="form.youtube" />
  </section>
  <section v-show="category === 'scheduling'" class="panel settings-panel" :aria-label="t('并发调度设置')">
   <div class="form-grid">
    <label>{{ t("YouTube 并发") }}<input v-model.number="form.youtube_concurrent" type="number" min="1" required></label>
    <label>{{ t("Instagram 并发") }}<input v-model.number="form.instagram_concurrent" type="number" min="1" required></label>
   </div>
  </section>
  <section v-show="category === 'debug'" class="panel settings-panel" :aria-label="t('调试设置')">
   <div class="debug-settings">
    <div class="form-row"><label for="debug-enabled">{{ t('调试总开关') }}</label><ToggleSwitch v-model="debugEnabled" input-id="debug-enabled" :disabled="store.busy" /></div>
    <div class="form-row"><label for="debug-limit">{{ t('测试条目限制') }}</label>
     <div class="debug-limit-control"><span>{{ t('仅处理：') }}</span><span class="debug-limit-input">
      <input id="debug-limit" :value="debugLimit" type="text" inputmode="numeric" pattern="0*[1-9][0-9]*" :required="debugEnabled" :disabled="!debugEnabled || store.busy || category !== 'debug'" @input="setDebugLimit">
      <span class="debug-limit-suffix" aria-hidden="true">{{ t('条') }}</span>
     </span></div>
    </div>
   </div>
  </section>
 <div class="settings-save"><button class="primary" :disabled="store.busy">{{ store.busy ? t("正在保存…") : t("保存设置") }}</button></div>
 </form>
 <section v-show="category === 'cookies'" class="panel settings-panel" :aria-label="t('Cookie 配置')">
  <div v-for="p in ['youtube', 'instagram']" :key="p" class="settings-row settings-cookie-row">
   <strong>{{ p === 'youtube' ? 'YouTube' : 'Instagram' }}</strong>
   <span class="cookie-status" :class="{ configured: cookies[p] }">{{ cookies[p] ? t("已配置") : t("未配置") }}</span>
   <div class="settings-row-actions"><button :disabled="store.busy" @click="editCookie(p)">{{ t("编辑") }}</button>
   <button :disabled="store.busy || !cookies[p]" @click="removeCookie(p)">{{ t("删除") }}</button></div>
  </div>
 </section>
 <section v-show="category === 'engines'" class="panel settings-panel" :aria-label="t('运行引擎')">
  <div class="settings-engine-actions"><button :disabled="store.busy" @click="store.mutate(loadVersions, t('版本已刷新'))">{{ t("刷新版本") }}</button></div>
  <div v-for="(version, name) in versions" :key="name" class="settings-row settings-version-row"><strong>{{ name }}</strong><span>{{ version }}</span></div>
  <details class="settings-details"><summary>{{ t("Instagram 引擎高级配置") }}</summary>
   <div class="settings-detail-content"><textarea v-model="galleryConfig" class="code" rows="8" spellcheck="false" :aria-label="t('gallery-dl JSON 配置')"></textarea>
   <div class="settings-inline-actions"><button :disabled="store.busy" @click="store.mutate(() => api('/gallery-config', 'PUT', { config: galleryConfig }), t('引擎配置已保存'))">{{ t("保存引擎配置") }}</button></div></div>
  </details>
 </section>
 <StorageManagement v-if="category === 'storage'" />
 <div v-if="cookiePlatform" class="modal-backdrop"><section class="modal" role="dialog" aria-modal="true" aria-labelledby="cookie-title">
  <div class="section-top"><h2 id="cookie-title">{{ cookiePlatform === 'instagram' ? 'Instagram' : 'YouTube' }} Cookie</h2><button class="icon-button" :aria-label="t('关闭 Cookie 编辑')" @click="closeCookie">×</button></div>
  <form class="settings-cookie-form" @submit.prevent="saveCookie"><textarea v-model="cookieText" class="code" rows="10" spellcheck="false" :aria-label="t('Cookie 内容')" placeholder="sessionid=...; csrftoken=..." required></textarea>
   <div class="form-actions"><button type="button" @click="closeCookie">{{ t("取消") }}</button><button class="primary" :disabled="store.busy">{{ store.busy ? t("正在保存…") : t("保存") }}</button></div>
  </form>
 </section></div>
</template>
