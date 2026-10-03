// index.js 的类型（D-067）：三个端的 vite.config.ts 用。
import type { Plugin } from 'vite'

/** 给每次构建一个编号：写进 import.meta.env.VITE_GA_BUILD，并生成 dist/version.json。开发服务器上为空。 */
export declare function buildId(): Plugin

/** 生成 third-party-licenses.txt：打进产物的每个第三方包的许可证和 NOTICE（D-027）；找不到许可证文件时构建失败（D-028）。 */
export declare function thirdPartyLicenses(opts?: { fileName?: string; root?: string }): Plugin

/** 从模块路径找出它所在的包目录；不在 node_modules 里时返回 null。 */
export declare function packageDirOf(id: string): string | null
