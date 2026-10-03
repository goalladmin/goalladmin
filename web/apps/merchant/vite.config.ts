import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { buildId, thirdPartyLicenses } from '@ga/shell/vite'

// 开发和预览都把 /api 反代到商户端的程序（本机 make run-merchant，默认 :8082），前后端同源（规范 §5.2）。后端地址用 GA_API_TARGET 覆盖。
const apiTarget = process.env.GA_API_TARGET ?? 'http://127.0.0.1:8082'
const proxy = { '/api': { target: apiTarget, changeOrigin: false } }

export default defineConfig({
  // 构建时生成 dist/third-party-licenses.txt（D-027）和 dist/version.json（构建编号），插件在壳里三端共用（D-067）
  plugins: [vue(), thirdPartyLicenses(), buildId()],
  server: { port: 5175, proxy },
  preview: { port: 4176, proxy },
  build: {
    outDir: 'dist',
    sourcemap: false,
    // 主包里是 Element Plus 全量和 11 种语言的文案（D-026）
    chunkSizeWarningLimit: 2000,
  },
})
