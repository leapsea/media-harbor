<script setup lang="ts">
import { t } from '../i18n'
import { computed } from 'vue'
import { pageSizes, pageWindow } from '../utils/pagination'

/** 共用分页交互，列表仅传递数量和本次浏览状态，不重复计算页码。 */
const props = defineProps<{ total: number; page: number; size: number }>()
const emit = defineEmits<{ 'update:page': [value: number]; 'update:size': [value: number] }>()
const window = computed(() => pageWindow(props.total, props.size, props.page))
</script>

<template>
 <nav class="list-pagination" :aria-label="t('列表分页')">
  <span class="help">{{ t('共 {count} 条', { count: total }) }}<span v-if="total"> · {{ window.start + 1 }}–{{ window.end }}</span></span>
  <label>{{ t("每页") }}<select :value="size" :aria-label="t('每页展示条数')" @change="emit('update:size', Number(($event.target as HTMLSelectElement).value))"><option v-for="value in pageSizes" :key="value" :value="value">{{ t('{count} 条', { count: value }) }}</option><option v-if="!pageSizes.includes(size)" :value="size">{{ t('{count} 条', { count: size }) }}</option></select></label>
  <div class="pagination-buttons">
   <button :disabled="window.current === 1" :aria-label="t('上一页')" @click="emit('update:page', window.current - 1)">{{ t("上一页") }}</button>
   <label class="page-picker"><select :value="window.current" :aria-label="t('跳转到页')" @change="emit('update:page', Number(($event.target as HTMLSelectElement).value))"><option v-for="value in window.pages" :key="value" :value="value">{{ value }} / {{ window.pages }}</option></select></label>
   <button :disabled="window.current === window.pages" :aria-label="t('下一页')" @click="emit('update:page', window.current + 1)">{{ t("下一页") }}</button>
  </div>
 </nav>
</template>
