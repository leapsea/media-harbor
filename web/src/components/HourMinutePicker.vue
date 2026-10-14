<script setup lang="ts">
import DatePicker from 'primevue/datepicker'
import { computed } from 'vue'
/** 对外只编辑HH:mm，组件内部Date只承载小时分钟，不转换为UTC或业务日期。 */
const props = defineProps<{ modelValue: string; inputId: string; label: string; disabled?: boolean; required?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()
/** 仅把有效HH:mm交给控件；空值或无效值以未选择状态打开，允许鼠标重新选择。 */
const time = computed(() => {
 if (!/^(?:[01]\d|2[0-3]):[0-5]\d$/.test(props.modelValue)) return null
 const [hour, minute] = props.modelValue.split(':').map(Number)
 // 固定本地日期避开浏览器当天的夏令时切换，不把休眠窗口转换为UTC。
 return new Date(2000, 0, 1, hour, minute, 0)
})
/** 无效Date仍属于Date实例，必须检查时间戳，避免NaN:NaN写回并污染后续选择。 */
function select(value: unknown) {
 if (value instanceof Date && Number.isFinite(value.getTime())) {
  emit('update:modelValue', `${String(value.getHours()).padStart(2, '0')}:${String(value.getMinutes()).padStart(2, '0')}`)
 }
}
/* 历史资产：new Date(2000, 0, 1, hour, minute, 0) 未校验输入；
   select 仅检查 instanceof Date，无效日期也会写回 NaN:NaN。 */
</script>
<template>
 <!-- 两个休眠字段共用24小时制及弹框设置，日期和秒均不参与选择。 -->
 <DatePicker class="hour-minute-picker" :model-value="time" @update:model-value="select"
  :input-id="inputId" :aria-label="label" :disabled="disabled" :required="required"
  time-only hour-format="24" :show-seconds="false" :manual-input="false"
  show-icon icon-display="input" panel-class="hour-minute-panel">
  <template #inputicon="{ clickCallback }">
   <svg class="hour-minute-clock" viewBox="0 0 16 16" aria-hidden="true" @click="clickCallback">
    <circle cx="8" cy="8" r="6" fill="none" stroke="currentColor" />
    <path d="M8 4v4l3 2" fill="none" stroke="currentColor" stroke-linecap="round" />
   </svg>
  </template>
 </DatePicker>
</template>
