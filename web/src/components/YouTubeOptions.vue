<script setup lang="ts">
import { t } from '../i18n'
import type { YouTubeOptions } from '../types'
/**
 * 表单值由父组件持有；以新对象向上提交修改。
 */
const props = defineProps<{ modelValue: YouTubeOptions; defaults?: YouTubeOptions }>()
const emit = defineEmits<{ 'update:modelValue': [value: YouTubeOptions] }>()
const toggles: { key: keyof YouTubeOptions; label: string }[] = [
 { key: 'video', label: '下载视频' }, { key: 'subtitles', label: '人工字幕' },
 { key: 'auto_subtitles', label: '自动字幕' },
 { key: 'comments', label: '评论（JSON）' }, { key: 'extra_audio', label: '额外音频（原始最高品质）' },
 { key: 'extract_audio', label: '从本地视频提取音频' },
 { key: 'thumbnail', label: '缩略图' }, { key: 'metadata', label: '元数据' },
]
/** 画质选项同时约束独立视频与合并格式，避免目标分辨率回退到更高画质。 */
const formats = [
 { label: '最高画质', value: 'bestvideo+bestaudio/best' },
 ...[2160, 1440, 1080, 720, 480].map(height => ({
  label: height === 2160 ? '4K（2160p）' : height === 1440 ? '2K（1440p）' : `${height}p`,
  value: `bestvideo[height<=${height}]+bestaudio/best[height<=${height}]`,
 })),
]
/** 兼容已有订阅的自定义表达式，打开表单不会隐式改写原配置。 */
function knownFormat(format: string) { return formats.some(option => option.value === format) }
/**
 * 两种音频操作独立提交，后端不会将其中一种替换成另一种。
 */
function set(key: keyof YouTubeOptions, value: string | boolean | number) {
 emit('update:modelValue', { ...props.modelValue, [key]: value })
}
function value(event: Event) { return (event.target as HTMLInputElement).value }
function checked(event: Event) { return (event.target as HTMLInputElement).checked }
</script>
<template>
 <div class="options-grid">
  <label v-for="toggle in toggles" :key="toggle.key" class="check">
   <input type="checkbox" :checked="Boolean(modelValue[toggle.key])" @change="set(toggle.key, checked($event))">{{ t(toggle.label) }}
  </label>
 </div>
 <div class="form-grid">
  <label>{{ t("视频画质") }}<select :value="modelValue.format" @change="set('format', value($event))"><option v-for="format in formats" :key="format.value" :value="format.value">{{ t(format.label) }}</option><option v-if="!knownFormat(modelValue.format)" :value="modelValue.format">{{ t("自定义（已有配置）") }}</option></select></label>
  <label>{{ t("字幕语言") }}<input :value="modelValue.subtitle_languages" @input="set('subtitle_languages', value($event))"></label>
  <!-- 旧订阅缺省时回显系统值，数量包含回复，达到上限即停止分页。 -->
  <label v-if="modelValue.comments">{{ t('最新评论数量（含回复）') }}<input type="number" min="1" step="1" required :value="modelValue.comment_limit || defaults?.comment_limit || 100" @input="set('comment_limit', Number(value($event)))"></label>
  <label v-if="modelValue.comments">{{ t('评论超时（秒）') }}<input type="number" min="1" step="1" required :value="modelValue.comment_timeout_seconds || defaults?.comment_timeout_seconds || 180" @input="set('comment_timeout_seconds', Number(value($event)))"></label>
  <!-- 格式与码率仅控制本地提取，额外下载音频始终保留最佳源音轨。 -->
  <label v-if="modelValue.extract_audio">{{ t("本地提取格式") }}<select :value="modelValue.audio_format" @change="set('audio_format', value($event))"><option value="best">{{ t("原始格式（不转码）") }}</option><option>mp3</option><option>m4a</option><option>flac</option></select></label>
  <label v-if="modelValue.extract_audio && modelValue.audio_format !== 'best'">{{ t("转码音频质量") }}<select :value="modelValue.audio_quality" @change="set('audio_quality', value($event))"><option value="0">{{ t("最高") }}</option><option value="320K">320 kbps</option><option value="256K">256 kbps</option><option value="192K">192 kbps</option><option value="128K">128 kbps</option></select></label>
  <!-- 历史资产：音频格式和音频质量原先同时影响额外下载与本地提取。 -->
  <label>{{ t("任务内部片段并发") }}<input type="number" min="1" :value="modelValue.fragments" @input="set('fragments', Number(value($event)))"></label>
 </div>
</template>
