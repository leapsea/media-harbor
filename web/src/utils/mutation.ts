/**
 * 写请求成功即确认业务结果；刷新只是展示同步，不延迟确认或改变写入结果。
 * 分开捕获错误，避免刷新失败被误报为写入失败，诱导用户重复提交。
 */
export async function confirmMutation(
 action: () => Promise<unknown>,
 refresh: () => Promise<unknown>,
 success: () => void,
 failure: (error: unknown) => void,
 refreshFailure: (error: unknown) => void,
): Promise<boolean> {
 try {
  await action()
 } catch (error) {
  failure(error)
  return false
 }
 success()
 // 刷新错误仍可见，但不会撤销服务端已经确认的入队、保存或删除结果。
 void refresh().catch(refreshFailure)
 return true
}
