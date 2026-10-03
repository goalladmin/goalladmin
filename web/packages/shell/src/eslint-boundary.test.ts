// 前端依赖方向的 lint 规则（D-064）：应用 src/ 下的源码只能引用应用目录里的文件和 @ga/shell（包根、样式）。

import path from 'node:path'
import { Linter } from 'eslint'
import { describe, expect, it } from 'vitest'

import { boundary } from '../eslint/boundary.js'

// 应用在 web/apps/agent：同级的 web/apps/platform、web/apps/merchant 和 web/packages/shell 都在它外面
const root = path.resolve('/repo/web/apps/agent')

function lint(code: string, file = 'src/views/orders/index.ts', allow?: string[]): string[] {
  const linter = new Linter({ configType: 'flat', cwd: root })
  const config: Linter.Config[] = [{ languageOptions: { ecmaVersion: 'latest', sourceType: 'module' } }, boundary(root, allow)]
  return linter.verify(code, config, path.join(root, file)).map((m) => m.message)
}

describe('ga/boundary', () => {
  it('应用自己的文件、@ga/shell 的包根和样式、第三方包都可以', () => {
    const ok = [
      "import { createPortalApp } from '@ga/shell'",
      "import '@ga/shell/styles'",
      "import { ref } from 'vue'",
      "import a from './a'",
      "import b from '../../api/system'",
      "import pkg from '../../../package.json'",
      "import c from '/src/api/system'",
      "export { d } from '../d'",
      "const v = import('./detail.vue')",
      "const views = import.meta.glob('../../views/**/*.vue')",
      "const loc = import.meta.glob(['../../locales/*.ts', '!../../locales/x.ts'], { eager: true })",
    ]
    for (const code of ok) expect(lint(code), code).toEqual([])
  })

  it('别的应用、壳的内部文件、应用目录之外的路径一律报错', () => {
    // 文件在 src/views/orders/ 下：往上四级才出应用目录
    const bad = [
      "import '@ga/platform'",
      "import { x } from '@ga/merchant'",
      "import { createClient } from '@ga/shell/src/request/client'",
      "import api from '../../../../platform/src/api/system'",
      "export * from '../../../../merchant/src/api'",
      "export { y } from '../../../../../packages/shell/src/app'",
      "const m = import('../../../../platform/src/views/system/user/index.vue')",
      "const v = import.meta.glob('../../../../platform/src/views/**/*.vue')",
      "const w = import.meta.glob(['./a/*.vue', '../../../../merchant/src/views/**/*.vue'])",
      "import z from '/../platform/src/main'",
    ]
    for (const code of bad) {
      const msgs = lint(code)
      expect(msgs, code).toHaveLength(1)
      expect(msgs[0], code).toContain('D-064')
    }
  })

  it('只管 src/ 下的源码：构建配置可以引用壳的 lint 规则和应用目录之外的文件', () => {
    expect(lint("import { boundary } from '@ga/shell/eslint'", 'eslint.config.js')).toEqual([])
    expect(lint("import x from '../platform/build/build-id.js'", 'vite.config.js')).toEqual([])
  })

  it('允许名单可以扩充', () => {
    expect(lint("import '@ga/extra'", undefined, ['@ga/shell', '@ga/extra'])).toEqual([])
    expect(lint("import '@ga/shell/styles'", undefined, ['@ga/shell'])).toHaveLength(1)
  })
})
