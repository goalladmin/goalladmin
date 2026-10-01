// createPortalApp：一个端的全部装配。各端的 main.ts 只需要一行。

import { createApp, h } from 'vue'
import type { App as VueApp, Component } from 'vue'
import type { RouteRecordRaw } from 'vue-router'
import { createPinia } from 'pinia'
import ElementPlus, { ElButton, ElMessage, ElNotification } from 'element-plus'
import * as Icons from '@element-plus/icons-vue'
import 'element-plus/dist/index.css'
import 'element-plus/theme-chalk/dark/css-vars.css'
import './styles/index.css'

import GaRoot from './GaRoot.vue'
import GaLayout from './layout/GaLayout.vue'
import GaLogin from './views/GaLogin.vue'
import GaChangePassword from './views/GaChangePassword.vue'
import GaProfile from './views/GaProfile.vue'
import GaForbidden from './views/GaForbidden.vue'
import GaNotFound from './views/GaNotFound.vue'
import GaDictTag from './dict/GaDictTag.vue'
import { useAuthStore } from './auth/store'
import { apiPrefix, setPortalContext } from './context'
import { AllLocaleCodes, createPortalI18n } from './i18n'
import type { LocaleCode, LocaleMessages } from './i18n'
import { formatApiError } from './request/errors'
import { GaPerm, vPerm } from './perm'
import { createClient } from './request/client'
import { RouteNames, createPortalRouter } from './router'
import type { ViewGlob } from './router/menu'
import { Codes } from './types'
import type { ApiError } from './types'
import { watchVersion } from './version-watch'

export interface PortalAppOptions {
  /** 端编码，与后端 portal.Portal.Code 一致。 */
  portal: string
  /** import.meta.glob('./views/**\/*.vue') 的结果。 */
  views: ViewGlob
  /** 各端的文案（菜单标题、权限名、页面）。 */
  locales?: LocaleMessages
  /** 额外的静态路由（不走布局）。 */
  routes?: RouteRecordRaw[]
  /** 替换内置页面。 */
  pages?: Partial<{
    layout: Component
    login: Component
    changePassword: Component
    profile: Component
    forbidden: Component
    notFound: Component
  }>
  /** 部署子路径，默认 '/'。 */
  base?: string
  /** 启用的界面语言（D-026），按这个列表出现在语言选择里；不设时启用全部支持的语言。 */
  languages?: LocaleCode[]
  /** 这次构建的编号（端的构建脚本生成）：服务器上的 version.json 变了就提示刷新。不设时不检查。 */
  build?: string
  /** 读上传头像的接口路径（相对端的接口前缀，D-040），默认 /system/avatars。 */
  avatarPath?: string
}

export function createPortalApp(opts: PortalAppOptions): VueApp {
  const pinia = createPinia()
  const languages = opts.languages?.length ? opts.languages : [...AllLocaleCodes]
  const i18n = createPortalI18n(opts.locales, languages)
  const t = (key: string) => i18n.global.t(key)
  const tp = (key: string, params?: Record<string, unknown>) => i18n.global.t(key, params ?? {})
  const te = (key: string) => i18n.global.te(key) || i18n.global.te(key, 'en-US')

  const client = createClient({
    baseURL: apiPrefix(opts.portal),
    locale: () => String(i18n.global.locale.value),
    token: {
      get: () => useAuthStore(pinia).token,
      set: (tok) => useAuthStore(pinia).setToken(tok),
      clear: () => useAuthStore(pinia).clear(),
      epoch: () => useAuthStore(pinia).epoch,
      session: () => useAuthStore(pinia).sessionId,
    },
    onAuthLost: () => {
      const auth = useAuthStore(pinia)
      const wasAuthenticated = auth.authenticated
      auth.clear()
      if (wasAuthenticated) {
        ElMessage.warning(t('shell.error.sessionExpired'))
        void portalRouter.router.replace({ name: RouteNames.login, query: { redirect: portalRouter.router.currentRoute.value.fullPath } })
      }
    },
    onPwdChangeRequired: () => {
      void portalRouter.router.replace({ name: RouteNames.changePassword })
    },
    // 锁屏由服务端判定（D-027）：别的标签页锁了屏，这里的下一个请求就会拿到 1006，直接显示锁屏
    onSessionLocked: () => {
      useAuthStore(pinia).markLocked()
    },
    notify: (err: ApiError) => {
      if (err.code === Codes.PwdChangeRequired || err.code === Codes.SessionLocked) return
      ElMessage.error(err.code === -1 ? t('shell.error.network') : formatApiError(err, tp, te))
    },
  })

  const pages = {
    login: opts.pages?.login ?? GaLogin,
    changePassword: opts.pages?.changePassword ?? GaChangePassword,
    profile: opts.pages?.profile ?? GaProfile,
    forbidden: opts.pages?.forbidden ?? GaForbidden,
    notFound: opts.pages?.notFound ?? GaNotFound,
  }
  const portalRouter = createPortalRouter({
    views: opts.views,
    layout: opts.pages?.layout ?? GaLayout,
    pages,
    extraRoutes: opts.routes,
    base: opts.base,
  })

  setPortalContext({ portal: opts.portal, client, router: portalRouter.router, installMenuRoutes: portalRouter.installMenuRoutes, languages, avatarPath: opts.avatarPath })

  const app = createApp(GaRoot)
  app.use(pinia)
  app.use(i18n)
  app.use(ElementPlus)
  for (const [name, comp] of Object.entries(Icons)) app.component(name, comp as Component)
  app.directive('perm', vPerm)
  app.component('GaPerm', GaPerm)
  app.component('GaDictTag', GaDictTag)
  portalRouter.install(app)
  watchVersion({
    current: opts.build,
    url: `${(opts.base ?? '/').replace(/\/?$/, '/')}version.json`,
    onUpdate: () => {
      ElNotification({
        title: t('shell.update.title'),
        message: h('div', [
          h('p', { style: 'margin: 0 0 8px' }, t('shell.update.message')),
          h(ElButton, { type: 'primary', size: 'small', onClick: () => window.location.reload() }, () => t('shell.update.reload')),
        ]),
        type: 'info',
        duration: 0,
        position: 'bottom-right',
      })
    },
  })
  return app
}
