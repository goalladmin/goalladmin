/// <reference types="vite/client" />

declare module '*.vue' {
  import type { DefineComponent } from 'vue'
  const component: DefineComponent<object, object, unknown>
  export default component
}

interface ImportMetaEnv {
  /** 这次构建的编号（@ga/shell/vite 的 buildId 插件）；开发服务器上为空。 */
  readonly VITE_GA_BUILD: string
}
