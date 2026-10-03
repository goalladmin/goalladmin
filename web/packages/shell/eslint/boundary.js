// 前端的依赖方向（D-064）：一个端的应用只能引用自己目录里的文件和允许的工作区包（默认只有 @ga/shell 的包根和样式）。
// 对应后端的 scripts/depcheck.sh：pnpm 能挡住没声明的包名，挡不住 ../../../platform/src/... 这样跨到同级应用的相对路径。
//
// 用法（应用的 eslint.config.js）：
//
//	import { boundary } from '@ga/shell/eslint'
//	export default tseslint.config(..., boundary(import.meta.dirname))
//
// 查应用源码（src/ 下）里 import、export … from、动态 import() 和 import.meta.glob() 写死的路径。

import path from 'node:path'

/** 默认允许的工作区包：壳的包根和样式（壳的 package.json 只导出这两个给页面用）。 */
export const defaultAllow = ['@ga/shell', '@ga/shell/styles']

/**
 * 返回一段 ESLint 扁平配置：启用 ga/boundary 规则。
 * @param {string} root 应用的根目录（应用 eslint.config.js 所在目录，一般传 import.meta.dirname）
 * @param {string[]} [allow] 允许引用的 @ga/ 开头的包名（精确匹配）
 */
export function boundary(root, allow = defaultAllow) {
  const appRoot = path.resolve(root)
  const allowed = new Set(allow)

  /** 返回违反规则的原因；没问题返回空字符串。 */
  function violation(file, spec) {
    if (typeof spec !== 'string' || spec === '') return ''
    if (spec.startsWith('@ga/')) {
      return allowed.has(spec) ? '' : `只能引用 ${[...allowed].join('、')}，不能引用 ${spec}`
    }
    if (spec.startsWith('.') || path.isAbsolute(spec)) {
      // 以 / 开头的是相对应用根目录的路径（Vite 的写法），其余按文件所在目录解析
      const abs = spec.startsWith('/') ? path.join(appRoot, spec) : path.resolve(path.dirname(file), spec)
      const rel = path.relative(appRoot, abs)
      if (rel === '..' || rel.startsWith(`..${path.sep}`) || path.isAbsolute(rel)) {
        return `不能引用应用目录之外的文件：${spec}`
      }
    }
    return ''
  }

  const rule = {
    meta: {
      type: 'problem',
      docs: { description: '端的应用只能引用自己目录里的文件和 @ga/shell（D-064）' },
      schema: [],
    },
    create(context) {
      const file = context.filename
      const report = (node, spec) => {
        const why = violation(file, spec)
        if (why) context.report({ node, message: `前端依赖方向（D-064）：${why}` })
      }
      const fromSource = (node) => {
        if (node.source && node.source.type === 'Literal') report(node.source, node.source.value)
      }
      return {
        ImportDeclaration: fromSource,
        ExportNamedDeclaration: fromSource,
        ExportAllDeclaration: fromSource,
        ImportExpression(node) {
          if (node.source.type === 'Literal') report(node.source, node.source.value)
        },
        // import.meta.glob('…') / import.meta.glob(['…', '…'])：模式里写死的目录同样不能出应用
        CallExpression(node) {
          const c = node.callee
          if (
            c.type !== 'MemberExpression' ||
            c.property.type !== 'Identifier' ||
            !c.property.name.startsWith('glob') ||
            c.object.type !== 'MetaProperty'
          ) {
            return
          }
          const arg = node.arguments[0]
          const patterns = arg?.type === 'ArrayExpression' ? arg.elements : [arg]
          for (const p of patterns) {
            if (p?.type === 'Literal') report(p, String(p.value).replace(/^!/, ''))
          }
        },
      }
    },
  }

  // 只管应用的源码（src/ 下，含页面里的 <script>）：vite.config.ts、eslint.config.js 这些构建配置跑在 Node 里，
  // 本来就要引用构建工具和壳的 lint 规则
  return {
    files: ['src/**/*.{ts,js,mts,mjs,vue}'],
    plugins: { ga: { rules: { boundary: rule } } },
    rules: { 'ga/boundary': 'error' },
  }
}
