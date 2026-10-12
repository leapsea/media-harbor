<script setup lang="ts">
import { nextTick, onMounted, ref, watch } from 'vue'
import { t } from '../i18n'
/** 复用任务日志窗口尺寸，接收内存日志并保持最新行可见。 */
const props = defineProps<{ log: string; status: string; running: boolean }>()
const emit = defineEmits<{ close: [] }>()
const area = ref<HTMLTextAreaElement | null>(null)
onMounted(() => area.value?.focus())
/** 等待 DOM 更新后再滚动，避免新日志追加时停留在旧的底部位置。 */
watch(() => props.log, async () => { await nextTick(); if (area.value) area.value.scrollTop = area.value.scrollHeight })
</script>
<template>
 <div class="modal-backdrop dryrun-backdrop"><section class="modal log-modal" role="dialog" aria-modal="true" aria-labelledby="dryrun-title" @keydown.esc="emit('close')">
  <div class="section-top"><h2 id="dryrun-title">{{ t('Dryrun 日志') }}</h2><button class="icon-button" :aria-label="t('关闭日志')" @click="emit('close')">×</button></div>
  <textarea ref="area" class="log-content" :aria-label="t('Dryrun 日志')" :value="log" readonly spellcheck="false"></textarea>
  <footer class="form-actions"><span role="status" :aria-live="running ? 'polite' : 'off'">{{ status }}</span><button @click="emit('close')">{{ running ? t('停止并关闭') : t('关闭') }}</button></footer>
 </section></div>
</template>
