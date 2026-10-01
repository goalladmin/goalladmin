// Vite 插件：给每次构建一个编号。
//
// 编号写进代码（import.meta.env.VITE_GA_BUILD），同时生成 dist/version.json。前端定期读 version.json，
// 发现和自己不一样，就说明服务器上换了新版本（重新部署了），提示"系统已更新，请刷新"。开发服务器上不生成。

import { randomBytes } from 'node:crypto'

import type { Plugin } from 'vite'

export function buildId(): Plugin {
  const id = `${Date.now().toString(36)}-${randomBytes(4).toString('hex')}`
  let serve = false
  return {
    name: 'ga-build-id',
    config(_, env) {
      serve = env.command === 'serve'
      return { define: { 'import.meta.env.VITE_GA_BUILD': JSON.stringify(serve ? '' : id) } }
    },
    generateBundle() {
      if (serve) return
      this.emitFile({ type: 'asset', fileName: 'version.json', source: JSON.stringify({ build: id }) + '\n' })
    },
  }
}
