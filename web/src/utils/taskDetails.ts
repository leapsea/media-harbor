import type { DetailContent } from '../types.ts'

/** 文件必须严格大于 5 MB 才展示进度条，小文件的点固定为正文高度的三分之一。 */
export const largeFileBytes = 5 * 1024 * 1024
export const statusDotPixels = Math.round(14 / 3)
const active = (status: string) => status === 'running' || status === 'queued'

/** 保持列表顺序，将最早未结束的内容对齐到可视窗口顶部，待执行项同样可作为锚点。 */
export function detailAnchor(items: DetailContent[]): string {
 return items.find(item => active(item.status))?.key || ''
}

/** 下载大小与 UI 语言无关，展示单位保持紧凑且不伪造未知字节数。 */
export function transferSize(bytes: number): string {
 if (bytes < 1024) return `${bytes} B`
 if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
 return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}
