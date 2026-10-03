// 三个端共用的构建插件（D-027、D-028、D-067）：各端的 vite.config.ts 从 '@ga/shell/vite' 引用。
// 纯 JS：构建时由 Node 直接加载（Vite 打包配置文件时不打包工作区里链接进来的包），不进浏览器端的包。
// 目录不叫 vite/：壳的 tsconfig 有 baseUrl，同名目录会盖住 vite 包本身的类型。

export { buildId } from './build-id.js'
export { packageDirOf, thirdPartyLicenses } from './third-party-licenses.js'
