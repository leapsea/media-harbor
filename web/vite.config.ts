import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

/** 前端开发代理与容器同源 API 使用相同路径。 */
export default defineConfig({
 plugins: [vue()],
 server: { proxy: { '/api': 'http://127.0.0.1:8080' } },
})
