/** 页面与设置分类使用固定地址，刷新与分享无需服务端路由配置。 */
export const pageIds = ['overview', 'tasks', 'history', 'settings'] as const
export const settingIds = ['display', 'files', 'youtube', 'scheduling', 'cookies', 'engines', 'storage', 'debug'] as const
export type PageId = typeof pageIds[number]
export type SettingId = typeof settingIds[number]
export type HashRoute = { page: PageId; category: SettingId }

/** 未知地址回到总览，未知设置分类回到界面设置。 */
export function parseHashRoute(hash: string): HashRoute {
 const [page, category] = hash.replace(/^#\/?/, '').split('/')
 return {
  page: pageIds.includes(page as PageId) ? page as PageId : 'overview',
  category: settingIds.includes(category as SettingId) ? category as SettingId : 'display',
 }
}

export function routeHash(route: HashRoute): string {
 return route.page === 'settings' ? `#/settings/${route.category}` : `#/${route.page}`
}
