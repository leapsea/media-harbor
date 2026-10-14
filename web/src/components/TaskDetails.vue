<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { t } from '../i18n'
import type { Subscription } from '../types'
import { useMediaStore } from '../stores/media'
import { useTaskDetails } from '../composables/useTaskDetails'
import { detailAnchor, largeFileBytes, statusDotPixels, transferSize } from '../utils/taskDetails'

/** 连续列表按执行时间从上到下排列，附属文件仍归属于视频或帖子。 */
const props = defineProps<{ task: Subscription }>()
const emit = defineEmits<{ close: [] }>()
const store = useMediaStore()
const { details, loading } = useTaskDetails(props.task.id, store.reportError)
const body = ref<HTMLElement>()
const tailSpace = ref(0)
const closeButton = ref<HTMLButtonElement>()
const opener = document.activeElement as HTMLElement | null
/** 动态弹窗显式接管焦点，使 Escape 可用；关闭后回到原来的操作按钮。 */
onMounted(() => closeButton.value?.focus())
onUnmounted(() => opener?.focus())
const labels: Record<string, string> = { queued: '待处理', running: '下载中', completed: '已完成', failed: '失败', cancelled: '已取消', skipped: '无需下载', blocked: '未执行' }
const anchor = computed(() => detailAnchor(details.value.items))
/** 只在执行位置或列表顺序变化时推进窗口，字节轮询不会打断手动查看历史。 */
const windowKey = computed(() => `${anchor.value}|${details.value.items.map(item => item.key).join('|')}`)
let initialized = false
watch(windowKey, async () => {
 await nextTick()
 if (!body.value) return
 tailSpace.value = 0
 await nextTick()
 const viewport = body.value
 const target = Array.from(viewport.querySelectorAll<HTMLElement>('.detail-item')).find(item => item.dataset.contentKey === anchor.value)
 const top = target ? target.getBoundingClientRect().top - viewport.getBoundingClientRect().top + viewport.scrollTop : viewport.scrollHeight
 /** 最后一个活动内容较短时补足滚动空间，使已结束内容能退出可视窗口。 */
 if (target) tailSpace.value = Math.max(0, viewport.clientHeight - (viewport.scrollHeight - top))
 await nextTick()
 viewport.scrollTo({ top, behavior: initialized ? 'smooth' : 'auto' })
 initialized = true
}, { flush: 'post' })
</script>

<template>
 <div class="modal-backdrop" @keydown.esc="emit('close')">
  <section class="modal details-modal" role="dialog" aria-modal="true" aria-labelledby="task-details-title" tabindex="-1">
   <div class="section-top"><h2 id="task-details-title">{{ t('任务详情') }} · {{ task.display_name }}</h2><button ref="closeButton" class="icon-button" :aria-label="t('关闭')" @click="emit('close')">×</button></div>
   <div ref="body" class="detail-body" :aria-busy="loading">
    <p v-if="loading" class="empty">{{ t('加载中') }}</p>
    <p v-else-if="!details.items.length" class="empty">{{ t('暂无下载任务') }}</p>
    <article v-for="item in details.items" :key="item.key" class="detail-item" :data-content-key="item.key">
     <!-- 父任务直到全部附属阶段结束才完成；媒体流的 100% 不替代父状态。 -->
     <div class="detail-heading"><span class="badge">{{ t('任务') }}</span><strong>{{ item.title }}</strong><!-- 完成状态与资源数量直接来自帖子任务，清单未知时不把可见下载资源任务数量当作总数。 -->
      <small class="detail-manifest">{{ item.manifest_complete ? `${item.completed}/${item.expected}` : t('清单待确认') }}</small><span class="badge" :class="item.status">{{ item.status === 'skipped' ? t('已跳过') : t(labels[item.status]) }}</span></div>
     <small v-if="item.error" class="detail-error">{{ item.error }}</small>
     <div v-for="part in item.parts" :key="`${part.task_id}-${part.kind}`" class="detail-part">
      <!-- 小文件以五像素点表示状态，未知大小不填入估算百分比。 -->
      <div class="detail-part-top"><div class="detail-part-labels"><span v-for="label in part.labels" :key="label" class="badge">{{ t(label) }}{{ part.number ? ` ${part.number}` : '' }}</span></div><span class="detail-state" :class="part.status"><i v-if="part.bytes_total <= largeFileBytes" class="detail-dot" :style="{ width: `${statusDotPixels}px`, height: `${statusDotPixels}px` }" aria-hidden="true"></i>{{ item.status === 'skipped' && part.status === 'skipped' ? t('已跳过') : t(labels[part.status]) }}</span></div>
      <div v-if="part.bytes_total > largeFileBytes" class="detail-transfer"><progress :class="part.status" :value="Math.min(100, Math.max(0, part.percent))" max="100" :aria-label="part.labels.map(label => t(label)).join(' / ')"></progress><small>{{ Math.floor(part.percent) }}% · {{ transferSize(part.bytes_done) }} / {{ transferSize(part.bytes_total) }}</small></div>
      <!-- 失败信息保留在对应附属项下方，避免重新混入全局日志刷屏。 -->
      <small v-if="part.error" class="detail-error">{{ part.error }}</small>
     </div>
    </article>
    <div :style="{ height: `${tailSpace}px` }" aria-hidden="true"></div>
   </div>
  </section>
 </div>
</template>

<style scoped>
/** 独立卡片包住父任务与全部附属文件，卡片之间留白，避免子任务跨组混淆。 */
.details-modal{--detail-inset:16px;display:flex;flex-direction:column;overflow:hidden;padding:var(--detail-inset);gap:12px}
.section-top{gap:8px}.section-top h2{overflow-wrap:anywhere;min-width:0}
/** 滚动区域延伸到弹窗右侧，内边距留在卡片外，滚动条不会贴住卡片边框。 */
.detail-body{overflow:auto;min-height:0;max-height:65vh;overscroll-behavior:contain;overflow-anchor:none;margin-right:calc(4px - var(--detail-inset));padding:1px var(--detail-inset) 1px 1px;scrollbar-gutter:stable}
.detail-item{border:1px solid #dce5df;border-radius:8px;overflow:hidden;background:#fff;box-shadow:0 1px 3px #20343b08}
.detail-item+.detail-item{margin-top:12px}
/** 浅色标题区标明父任务，附属项使用轻分隔线，保持紧凑的两级层次。 */
.detail-heading{display:flex;align-items:flex-start;gap:8px;padding:10px 12px;background:#f3f6f4;border-bottom:1px solid #dce5df}.detail-manifest{align-self:center;color:#627778;white-space:nowrap;font-size:12px}.detail-heading strong{flex:1;min-width:0;overflow-wrap:anywhere;font-size:14px;line-height:20px}.detail-heading .badge{flex:none}
.detail-part{margin:0 12px;padding:8px 0}.detail-part+.detail-part{border-top:1px solid #edf1ee}
.detail-item>.detail-error{margin:8px 12px 0}
.detail-part-top{display:flex;gap:8px;align-items:center;justify-content:space-between}.detail-part-labels{display:flex;gap:4px;flex-wrap:wrap}
.detail-state{display:flex;align-items:center;gap:6px;font-size:12px;white-space:nowrap;color:#627778}
.detail-dot{display:inline-block;border-radius:50%;background:#388be0;flex:none}.completed .detail-dot{background:#24a47a}.failed .detail-dot{background:#d35d50}.cancelled .detail-dot,.skipped .detail-dot,.blocked .detail-dot{background:#97a4a7}
/** 大文件独立显示真实百分比；完成前后保持行高，避免轮询时布局跳动。 */
.detail-transfer{display:flex;flex-direction:column;gap:4px;margin-top:6px}.detail-transfer progress{width:100%;height:8px;border:0;border-radius:4px;overflow:hidden;appearance:none;background:#e9efeb;color:#388be0;accent-color:#388be0}.detail-transfer progress::-webkit-progress-bar{background:#e9efeb}.detail-transfer progress::-webkit-progress-value{background:#388be0;border-radius:4px}.detail-transfer progress.completed::-webkit-progress-value{background:#24a47a}.detail-transfer progress.failed::-webkit-progress-value{background:#d35d50}.detail-transfer progress::-moz-progress-bar{background:currentColor}.detail-transfer progress.completed{color:#24a47a;accent-color:#24a47a}.detail-transfer small{color:#627778;font-size:12px;text-align:right}.detail-error{display:block;margin-top:6px;color:#a73f37;overflow-wrap:anywhere}
@media(max-width:600px){.details-modal{--detail-inset:12px}.detail-heading{flex-wrap:wrap}.detail-heading strong{flex-basis:60%}}
</style>
