/** 同一资源复用未完成请求；不同页面或资源立即启动，不共享等待队列。 */
export function createRequestPool() {
 const pending = new Map<string, Promise<unknown>>()
 function run<T>(key: string, load: () => Promise<T>): Promise<T> {
  const existing = pending.get(key)
  if (existing) return existing as Promise<T>
  const request = Promise.resolve().then(load).finally(() => {
   if (pending.get(key) === request) pending.delete(key)
  })
  pending.set(key, request)
  return request
 }
 return { run }
}
