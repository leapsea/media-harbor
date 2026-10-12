/**
 * 从主页地址预填账号名称；内容链接等待后端读取作者，不使用资源 ID。
 */
export function directoryNameFromUrl(value: string): string {
 if (!value.trim()) return ''
 let url: URL
 try { url = new URL(value.trim()) } catch { return '' }
 const segments = url.pathname.split('/').filter(Boolean).map(segment => {
  try { return decodeURIComponent(segment) } catch { return segment }
 })
 const host = url.hostname.toLowerCase()
 let name = ''
 /**
  * 只有地址包含明确账号名称时预填；频道 ID、视频与播放列表不作为作者名。
  */
 if (host === 'youtu.be') name = ''
 else if (host === 'youtube.com' || host.endsWith('.youtube.com')) {
  name = segments[0]?.startsWith('@') ? segments[0].slice(1) : ['c', 'user'].includes(segments[0] || '') ? segments[1] || '' : ''
 } else if (host === 'instagram.com' || host.endsWith('.instagram.com')) name = ['p', 'reel', 'reels', 'tv', 'stories'].includes(segments[0] || '') ? '' : segments[0] || '' 
 else name = segments.at(-1) || url.hostname
 /**
  * 提取结果是单个目录名，去掉 URL 编码带来的路径分隔符。
  */
 return name.replace(/[\/\\]/g, '_')
}
