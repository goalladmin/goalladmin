import { createPortalApp } from '@ga/shell'

// 商户端（D-061、D-067）：按"编号 + 账号 + 密码"登录；概览、子账号、角色与权限、会话、日志、个人中心是壳的内置页面。
// 商户端的业务页面放在 src/views/ 下，路径就是后端菜单声明里的 Component；文案放在 src/locales/<语言代码>.ts（D-026）。
createPortalApp({
  portal: 'merchant',
  scoped: true,
  views: import.meta.glob('./views/**/*.vue'),
  // 构建编号：服务器换了新版本时提示刷新
  build: import.meta.env.VITE_GA_BUILD,
}).mount('#app')
