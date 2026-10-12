/** 地址决定执行方式；单条详情不提供周期执行选项。 */
export function taskMode(platform: string, address: string): 'profile' | 'content' {
 let url: URL
 try { url = new URL(address) } catch { return 'profile' }
 const parts = url.pathname.split('/').filter(Boolean)
 if (platform === 'youtube' && (url.searchParams.get('v') || (url.hostname === 'youtu.be' && parts[0]) || (parts[1] && ['shorts', 'live', 'embed'].includes(parts[0] || '')))) return 'content'
 if (platform === 'instagram' && parts[1] && (['p', 'reel', 'reels', 'tv'].includes(parts[0] || '') || (parts[0] === 'stories' && parts[2]))) return 'content'
 return 'profile'
}
