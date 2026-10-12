import type { ContentFile } from '../types'

/** 简写只用于展示，保留内容 ID 和真实文件后缀；完整路径用于复制文件夹位置。 */
export function artifactName(path: string, remoteID: string, title: string) {
 const name = path.split('/').pop() || path
 const idAt = remoteID ? name.lastIndexOf(remoteID) : -1
 if (idAt >= 0) return name.slice(idAt)
 // 引擎不包含 ID 时，去掉与记录标题完全相同的前缀。
 if (title && name.startsWith(title) && name.length > title.length) {
  return name.slice(title.length).replace(/^[-_.\s]+/, '') || name
 }
 return name
}

/** 优先复制视频、音频所在目录；图片记录使用其产物目录，重复目录只保留一次。 */
export function artifactDirectories(files: ContentFile[], paths: string[]) {
 const media = files.filter(file => file.tags?.some(tag => tag === 'video' || tag === 'audio'))
 const selected = media.length ? media.map(file => file.path) : paths
 return [...new Set(selected.map(path => path.slice(0, path.lastIndexOf('/')) || '/'))]
}
