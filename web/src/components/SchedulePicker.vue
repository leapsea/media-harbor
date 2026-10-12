<script setup lang="ts">
import { t } from '../i18n'
import { ref, watch } from 'vue'
import { encodeSchedule, parseSchedule } from '../utils/schedule'

/**
 * 表单只暴露鼠标可选的计划，生成的表达式通过 v-model 交给原有保存流程。
 */
const props = defineProps<{ modelValue: string; immediate?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()
const choice = ref(parseSchedule(props.modelValue))
const weekdays = ['周日', '周一', '周二', '周三', '周四', '周五', '周六']
const pad = (value: number) => String(value).padStart(2, '0')

/**
 * 仅在用户操作后写回，组件首次展示不会改写已有的自定义计划。
 */
function update() { emit('update:modelValue', encodeSchedule(choice.value)) }

/**
 * 外部切换订阅时刷新控件；自身写回则保留当前选项，避免周期切换造成抖动。
 */
watch(() => props.modelValue, value => {
 if (value !== encodeSchedule(choice.value)) choice.value = parseSchedule(value)
})
</script>

<template>
 <div class="form-row schedule-picker">
  <label for="schedule-mode">{{ immediate ? t("执行方式") : t("执行周期") }}</label>
  <div v-if="immediate" class="schedule-body"><select id="schedule-mode" :aria-label="t('执行方式')" disabled><option>{{ t("立即执行") }}</option></select></div>
  <div v-else class="schedule-body">
   <div class="schedule-line">
   <select id="schedule-mode" v-model="choice.mode" :aria-label="t('执行周期')" @change="update">
    <option value="manual">{{ t("仅手动执行") }}</option>
    <option value="interval">{{ t("按间隔执行") }}</option>
    <option value="daily">{{ t("每天固定时间") }}</option>
    <option value="weekly">{{ t("每周固定时间") }}</option>
    <option v-if="choice.original && parseSchedule(choice.original).mode === 'preserve'" value="preserve">{{ t("保留已有自定义周期") }}</option>
   </select>
  <!-- 间隔、日期和时间均使用选择控件，无需输入调度表达式。 -->
  <div v-if="choice.mode === 'interval'" class="schedule-controls">
   <span>{{ t("每") }}</span>
   <select v-model="choice.interval" :aria-label="t('间隔数量')" @change="update"><option v-for="n in 60" :key="n" :value="n">{{ n }}</option></select>
   <select v-model="choice.unit" :aria-label="t('间隔单位')" @change="update"><option value="m">{{ t("分钟") }}</option><option value="h">{{ t("小时") }}</option></select>
   <span>{{ t("执行") }}</span>
  </div>
  <div v-else-if="choice.mode === 'daily' || choice.mode === 'weekly'" class="schedule-controls">
   <select v-if="choice.mode === 'weekly'" v-model="choice.weekday" :aria-label="t('执行星期')" @change="update"><option v-for="(day, index) in weekdays" :key="day" :value="index">{{ t(day) }}</option></select>
   <select v-model="choice.hour" :aria-label="t('执行小时')" @change="update"><option v-for="n in 24" :key="n" :value="n - 1">{{ pad(n - 1) }}{{ t("时") }}</option></select>
   <select v-model="choice.minute" :aria-label="t('执行分钟')" @change="update"><option v-for="n in 60" :key="n" :value="n - 1">{{ pad(n - 1) }}{{ t("分") }}</option></select>
  </div>
   </div>
  <small v-if="choice.mode === 'preserve'" class="schedule-note">{{ t("当前周期：") }}{{ choice.original }}</small>
  </div>
 </div>
</template>
