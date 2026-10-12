import { createApp } from 'vue'
import { createPinia } from 'pinia'
import PrimeVue from 'primevue/config'
import ToastService from 'primevue/toastservice'
import Aura from '@primeuix/themes/aura'
import App from './App.vue'
import './style.css'

/**
 * 应用只有一个实例，Pinia 收口服务器数据和用户操作状态。
 */
createApp(App)
 .use(createPinia())
 .use(PrimeVue, { theme: { preset: Aura, options: { darkModeSelector: false, cssLayer: { name: 'primevue', order: 'primevue, app' } } } })
 .use(ToastService)
 .mount('#app')
