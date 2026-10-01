import { defineAsyncComponent } from 'vue'
import { createPortalApp, localesFromGlob } from '@ga/shell'

createPortalApp({
  portal: 'platform',
  views: import.meta.glob('./views/**/*.vue'),
  // 每种语言一个文件（locales/<语言代码>.ts），D-026
  locales: localesFromGlob(import.meta.glob('./locales/*.ts', { eager: true })),
  // 构建编号：服务器换了新版本时提示刷新
  build: import.meta.env.VITE_GA_BUILD,
  // 个人中心用平台端自己的页面（D-038）：壳的默认页只有账号和权限码。按需加载，和 views 的动态导入落在同一个分块里
  pages: { profile: defineAsyncComponent(() => import('./views/profile/index.vue')) },
}).mount('#app')
