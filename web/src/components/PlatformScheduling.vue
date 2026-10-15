<script setup lang="ts">
import { computed } from 'vue'
import ToggleSwitch from 'primevue/toggleswitch'
import HourMinutePicker from './HourMinutePicker.vue'
import { t } from '../i18n'
import type { Settings } from '../types'
import { withSleepEnabled } from '../utils/sleep'

/** 平台卡片编辑同一份表单，窗口、请求预算与下载线程均按平台定位。 */
const props = defineProps<{ platform: 'instagram' | 'youtube'; disabled: boolean }>()
const settings = defineModel<Settings>({ required: true })
const sleep = computed(() => settings.value.sleep[props.platform])
const producer = computed(() => settings.value.pipeline[props.platform])
/** 只有开启时补齐缺席字段；关闭保留原值，重新开启仍回显已有窗口。 */
function setSleepEnabled(enabled: boolean) {
 settings.value.sleep[props.platform] = withSleepEnabled(sleep.value, enabled)
}
/** 并发字段保留原保存结构，重排界面不会改变消费者计数语义。 */
const concurrency = computed({
 get: () => settings.value[props.platform === 'instagram' ? 'instagram_concurrent' : 'youtube_concurrent'],
 set: value => { settings.value[props.platform === 'instagram' ? 'instagram_concurrent' : 'youtube_concurrent'] = value },
})
/** 可选择常用时区，也保留配置中已有的时区，避免保存时改变窗口含义。 */
const timezones = ['Asia/Singapore', 'Asia/Shanghai', 'Asia/Tokyo', 'Asia/Kolkata', 'Europe/Berlin', 'America/New_York', 'America/Los_Angeles', 'UTC']
const id = (field: string) => `${props.platform}-${field}`
</script>

<template>
 <section class="platform-scheduling" :aria-label="platform === 'instagram' ? 'Instagram' : 'YouTube'">
  <h3>{{ platform === 'instagram' ? 'Instagram' : 'YouTube' }}</h3>
  <!-- 常用的下载并发与休眠保持可见；请求预算和缓存放入高级区域。 -->
  <div class="form-row"><label :for="id('concurrency')">{{ t('下载并发') }}</label><input :id="id('concurrency')" v-model.number="concurrency" type="number" min="1" step="1" required :disabled="disabled"></div>
  <div class="platform-rest">
   <h4>{{ t('每日休眠') }}</h4>
   <div class="form-row"><label :for="id('sleep-enabled')">{{ t('启用休眠') }}</label><ToggleSwitch :model-value="sleep.enabled" @update:model-value="setSleepEnabled" :input-id="id('sleep-enabled')" :disabled="disabled" /></div>
   <!-- 历史资产：v-model="sleep.enabled" 仅修改开关，首次开启没有填充缺席时间与时区。 -->
   <!-- 每个平台独立使用24小时弹框，日期和秒不参与业务设置。 -->
   <div class="rest-times">
    <label :for="id('sleep-start')">{{ t('开始时间') }}<HourMinutePicker v-model="sleep.start" :input-id="id('sleep-start')" :label="t('开始时间')" :required="sleep.enabled" :disabled="!sleep.enabled || disabled" /></label>
    <label :for="id('sleep-end')">{{ t('结束时间') }}<HourMinutePicker v-model="sleep.end" :input-id="id('sleep-end')" :label="t('结束时间')" :required="sleep.enabled" :disabled="!sleep.enabled || disabled" /></label>
   </div>
   <div class="form-row"><label :for="id('sleep-timezone')">{{ t('休眠时区') }}</label><select :id="id('sleep-timezone')" v-model="sleep.timezone" :disabled="!sleep.enabled || disabled"><option v-for="zone in [...new Set([sleep.timezone, ...timezones])]" :key="zone" :value="zone">{{ zone }}</option></select></div>
   <small class="help">{{ t('仅影响当前平台；当前媒体完成后休眠，结束后自动继续。') }}</small>
  </div>
  <details class="platform-advanced">
   <summary>{{ t(platform === 'instagram' ? '请求节奏与缓存' : '请求节奏') }}</summary>
   <!-- 滑动窗口、最小间隔与429冷却分别控制生产者，不增加下载线程。 -->
   <div class="advanced-fields">
    <div class="form-row"><label :for="id('request-window')">{{ t('请求窗口（秒）') }}</label><input :id="id('request-window')" v-model.number="producer.window_seconds" type="number" min="1" step="1" required :disabled="disabled"></div>
    <div class="form-row"><label :for="id('request-limit')">{{ t('窗口请求上限') }}</label><input :id="id('request-limit')" v-model.number="producer.max_requests" type="number" min="1" step="1" required :disabled="disabled"></div>
    <div class="form-row"><label :for="id('request-interval')">{{ t('最小请求间隔（秒）') }}</label><input :id="id('request-interval')" v-model.number="producer.min_interval_seconds" type="number" min="0" step="0.1" required :disabled="disabled"></div>
    <div class="form-row"><label :for="id('request-cooldown')">{{ t('429 冷却（秒）') }}</label><input :id="id('request-cooldown')" v-model.number="producer.cooldown_seconds" type="number" min="0" step="0.1" required :disabled="disabled"></div>
    <!-- Instagram源缓存归入自身平台，不再与全局队列混在一个设置组中。 -->
    <template v-if="platform === 'instagram'">
     <div class="form-row"><label :for="id('cache-ttl')">{{ t('Instagram 缓存有效期（分钟）') }}</label><input :id="id('cache-ttl')" v-model.number="settings.pipeline.instagram_source_cache_ttl_minutes" type="number" min="1" step="1" required :disabled="disabled"></div>
     <div class="form-row"><label :for="id('cache-capacity')">{{ t('Instagram 缓存条数') }}</label><input :id="id('cache-capacity')" v-model.number="settings.pipeline.instagram_source_cache_capacity" type="number" min="1" step="1" required :disabled="disabled"></div>
    </template>
   </div>
  </details>
 </section>
</template>

<style scoped>
/* 平台卡片共用紧凑标签列，窄屏改为单列并保持标题和控件同行。 */
.platform-scheduling{display:grid;align-content:start;gap:12px;min-width:0;border:1px solid var(--line);border-radius:8px;padding:12px}
h3,h4{margin:0}h4{font-size:13px;color:var(--muted)}
.platform-rest,.advanced-fields{display:grid;gap:8px}
.platform-rest{border-top:1px solid var(--line);padding-top:12px}
.rest-times{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:12px}
.rest-times>label{display:grid;grid-template-columns:56px minmax(0,1fr);align-items:center;gap:8px;font-size:13px;color:var(--muted);min-width:0}
.form-row>label{text-align:right}.platform-advanced{border-top:1px solid var(--line);padding-top:8px}
/* 高级区保持鼠标可展开的焦点和悬停状态，不影响底部保存按钮。 */
summary{font-size:13px;color:var(--muted);cursor:pointer;line-height:22px;padding:4px 0}summary:hover{color:var(--ink)}
summary:focus-visible{outline:2px solid #dc997e;outline-offset:2px}.advanced-fields{padding-top:8px}
@media(max-width:600px){.rest-times{grid-template-columns:minmax(0,1fr)}.rest-times>label{grid-template-columns:120px minmax(0,1fr)}.rest-times>label{text-align:right}}
</style>
