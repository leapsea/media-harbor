import { ref, watch } from 'vue'
import en from './en.json'
import hi from './hi.json'
import ru from './ru.json'
import ja from './ja.json'

/** 中文为源语言；翻译只处理界面文案，不修改平台内容、文件或引擎日志。 */
export const languages = [
 { value: 'zh-CN', label: '简体中文' }, { value: 'en', label: 'English' },
 { value: 'hi', label: 'हिन्दी' }, { value: 'ru', label: 'Русский' }, { value: 'ja', label: '日本語' },
] as const
export type Language = typeof languages[number]['value']
export const locale = ref<Language>('zh-CN')
const messages: Record<string, Record<string, string>> = { en, hi, ru, ja }
/** 同步 html 语言以支持读屏和日期格式；选择只预览，持久保存由设置接口负责。 */
export function setLocale(value: string) {
 if (languages.some(item => item.value === value)) locale.value = value as Language
}
watch(locale, value => { document.documentElement.lang = value }, { immediate: true })
/** 命名参数保留完整句子结构，不拼接不同语言的语序。未知错误保留原始信息。 */
export function t(value: string | undefined, params: Record<string, string | number> = {}): string {
 const key = value || ''
 const text = locale.value === 'zh-CN' ? key : messages[locale.value]?.[key] ?? key
 return text.replace(/\{(\w+)\}/g, (match, name: string) => String(params[name] ?? match))
}
