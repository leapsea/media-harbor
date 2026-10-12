import { onUnmounted, ref } from 'vue'
import { t } from '../i18n'

/**
 * 日志只保留在当前弹窗内，不进入任务状态或浏览器持久存储。
 * 关闭窗口通过请求取消终止后台验证，下一次打开使用全新的日志。
 */
export function useDryRun() {
 const visible = ref(false)
 const running = ref(false)
 const log = ref('')
 const status = ref('')
 let controller: AbortController | undefined
 /** 每次打开独立连接，处理跨数据块的 JSON 行与 UTF-8 字符。 */
 async function start(payload: unknown) {
  if (running.value) return
  visible.value = true; running.value = true; log.value = ''; status.value = t('验证中…')
  controller = new AbortController()
  let completed = false
  try {
   const response = await fetch('/api/subscriptions/dryrun', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload), signal: controller.signal,
   })
   if (!response.ok || !response.body) throw new Error(t('请求失败'))
   const reader = response.body.getReader()
   /** 流式 UTF-8 解码保留跨块字符，按完整 JSON 行消费，避免切断中文日志。 */
   const decoder = new TextDecoder()
   let pending = ''
   const consume = (line: string) => {
    if (!line.trim()) return
    const event = JSON.parse(line) as { type: string; message?: string; ok?: boolean; error?: string }
    if (event.type === 'log') log.value += event.message || ''
    if (event.type === 'done') {
     /** 引擎报告的验证结果与连接是否成功建立分别判断。 */
     completed = true; status.value = event.ok ? t('验证通过') : t('验证失败')
     if (!event.ok && event.error) log.value += `${event.error}\n`
    }
   }
   while (true) {
    const chunk = await reader.read()
    pending += decoder.decode(chunk.value, { stream: !chunk.done })
    const lines = pending.split('\n'); pending = lines.pop() || ''
    lines.forEach(consume)
    if (chunk.done) break
   }
   consume(pending)
   /** 连接意外结束不等于验证通过，必须收到明确的结束事件。 */
   if (!completed) throw new Error(t('验证连接已中断'))
  } catch (error) {
   status.value = controller.signal.aborted ? t('已停止') : t('验证失败')
   if (!controller.signal.aborted) log.value += `${error instanceof Error ? error.message : String(error)}\n`
  } finally { running.value = false; controller = undefined }
 }
 function close() { controller?.abort(); visible.value = false }
 onUnmounted(close)
 return { visible, running, log, status, start, close }
}
