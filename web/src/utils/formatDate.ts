import { locale } from '../i18n'
/** 所有页面共用浏览器时区格式；未发生的时间统一显示占位符。 */
export function formatDate(value?: string) {
 return !value || value.startsWith('0001') ? '—' : new Date(value).toLocaleString(locale.value)
}
