import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

import { buildId, thirdPartyLicenses } from '@ga/shell/vite'

// 开发和预览都把 /api 反代到后端，前后端同源（规范 §5.2）。后端地址用 GA_API_TARGET 覆盖。
const apiTarget = process.env.GA_API_TARGET ?? 'http://127.0.0.1:8080'
const proxy = { '/api': { target: apiTarget, changeOrigin: false } }

export default defineConfig({
  // 构建时生成 dist/third-party-licenses.txt：打进包里的第三方库的许可证和 NOTICE（D-027）；
  // 以及 dist/version.json：这次构建的编号，前端发现服务器换了版本时提示刷新
  plugins: [vue(), thirdPartyLicenses(), buildId()],
  server: { port: 5173, proxy },
  preview: { port: 4173, proxy },
  build: {
    outDir: 'dist',
    sourcemap: false,
    // 主包里是 Element Plus 全量和 11 种语言的文案（D-026），1.6 MB 左右；首页的图表单独成块
    chunkSizeWarningLimit: 2000,
  },
})
