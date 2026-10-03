// boundary.js 的类型（D-064）：应用的 eslint.config.js 和壳的单元测试用。
import type { Linter } from 'eslint'

/** 默认允许的工作区包：壳的包根和样式。 */
export declare const defaultAllow: string[]

/** 返回启用 ga/boundary 规则的扁平配置：应用 src/ 下的源码只能引用应用目录里的文件和 allow 里的 @ga/ 包。 */
export declare function boundary(root: string, allow?: string[]): Linter.Config
