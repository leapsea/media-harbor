/** 所有列表和界面设置共享条数选项，默认值由服务端初始化设置提供。 */
export const pageSizes = [5, 8, 10, 20, 50, 100]

/** 总览使用较少的近期记录，列表分页仍沿用原有条数。 */
export const recentDownloadSizes = [3, ...pageSizes]

/** 删除末页记录后收回到有效页；空列表仍显示第 1 页。 */
export function pageWindow(total: number, size: number, page: number) {
 const pages = Math.max(1, Math.ceil(total / size))
 const current = Math.min(Math.max(1, page), pages)
 const start = (current - 1) * size
 return { pages, current, start, end: Math.min(start + size, total) }
}
