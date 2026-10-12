import { computed, onMounted, onUnmounted, ref } from 'vue'
import { parseHashRoute, routeHash } from '../utils/hashRoute'

/** 浏览器地址负责页面导航；监听 hashchange 同步前进、后退与手动输入。 */
export function useHashNavigation() {
 const route = ref(parseHashRoute(window.location.hash))
 const synchronize = () => { route.value = parseHashRoute(window.location.hash) }
 const page = computed({
  get: () => route.value.page,
  set: (page) => {
   route.value = { page, category: 'display' }
   window.location.hash = routeHash(route.value)
  },
 })
 const category = computed({
  get: () => route.value.category,
  set: (category) => {
   route.value = { page: 'settings', category }
   window.location.hash = routeHash(route.value)
  },
 })
 onMounted(() => {
  synchronize()
  // 初次规范地址只替换当前记录，不给后退列表增加一个空页面。
  window.history.replaceState(window.history.state, '', routeHash(route.value))
  window.addEventListener('hashchange', synchronize)
 })
 onUnmounted(() => window.removeEventListener('hashchange', synchronize))
 return { page, category }
}
