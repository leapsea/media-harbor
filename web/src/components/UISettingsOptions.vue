<script setup lang="ts">
import { t, languages, setLocale } from '../i18n'
import type { UISettings } from '../types'
import { pageSizes, recentDownloadSizes } from '../utils/pagination'
/** 展示设置以字段定义集中维护，共用条数选项，父级负责持久保存。 */
const props = defineProps<{ modelValue: UISettings }>()
const emit = defineEmits<{ 'update:modelValue': [value: UISettings] }>()
const fields: { key: Exclude<keyof UISettings, 'language'>; label: string; sizes: number[] }[] = [
 { key: 'recent_downloads', label: '总览最新下载', sizes: recentDownloadSizes },
 { key: 'history_page_size', label: '下载历史每页', sizes: pageSizes },
 { key: 'task_page_size', label: '任务管理每页', sizes: pageSizes },
 { key: 'task_log_lines', label: '任务日志行数', sizes: [50, 100, 200, 500, 1000] },
]
/** 提交完整界面配置副本，避免直接修改父级对象。 */
function set(key: Exclude<keyof UISettings, 'language'>, event: Event) {
 emit('update:modelValue', { ...props.modelValue, [key]: Number((event.target as HTMLSelectElement).value) })
}
/** 预览使用独立语言状态，轮询不会覆盖未保存的选择。 */
function changeLanguage(event: Event) {
 const language = (event.target as HTMLSelectElement).value
 setLocale(language)
 emit('update:modelValue', { ...props.modelValue, language })
}
</script>
<template>
 <div class="form-grid">
  <label>{{ t('界面语言') }}<select :value="modelValue.language" :aria-label="t('界面语言')" @change="changeLanguage($event)"><option v-for="language in languages" :key="language.value" :value="language.value">{{ language.label }}</option></select></label>
  <label v-for="field in fields" :key="field.key">{{ t(field.label) }}
   <select :value="modelValue[field.key]" :aria-label="t(field.label)" @change="set(field.key, $event)">
    <option v-for="size in field.sizes" :key="size" :value="size">{{ t(field.key === 'task_log_lines' ? '{count} 行' : '{count} 条', { count: size }) }}</option>
    <option v-if="!field.sizes.includes(modelValue[field.key])" :value="modelValue[field.key]">{{ t(field.key === 'task_log_lines' ? '{count} 行' : '{count} 条', { count: modelValue[field.key] }) }}</option>
   </select>
  </label>
 </div>
</template>
