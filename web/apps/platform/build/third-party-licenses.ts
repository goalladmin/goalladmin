// Vite 插件：生成 third-party-licenses.txt（D-027）。
//
// 收录实际打进产物的每个第三方包（按打包结果里的模块路径找到所在的包），附上它的 LICENSE / NOTICE 原文。
// MIT、Apache-2.0 等许可证要求分发时附带版权和许可声明；页面上不需要显示署名，放在部署目录里即可。
// 本仓库自己的代码（@ga/*）不收录。某个包找不到许可证文件时构建失败（D-028）：新引入的依赖必须先弄清许可证。

import { existsSync, readFileSync, readdirSync } from 'node:fs'
import { createRequire } from 'node:module'
import { dirname, join, sep } from 'node:path'

import type { Plugin } from 'vite'

interface Pkg {
  name: string
  version: string
  license: string
  dir: string
}

/** 从模块路径找出它所在的包目录（取最后一个 node_modules 之后的一段，带作用域的取两段）。 */
export function packageDirOf(id: string): string | null {
  const marker = `${sep}node_modules${sep}`
  const i = id.lastIndexOf(marker)
  if (i < 0) return null
  const rest = id.slice(i + marker.length).split(sep)
  const n = rest[0]?.startsWith('@') ? 2 : 1
  if (rest.length < n) return null
  return id.slice(0, i + marker.length) + rest.slice(0, n).join(sep)
}

/** 打包工具注入的虚拟模块（不在 node_modules 里）属于哪个包。 */
const VIRTUAL: [RegExp, string][] = [
  [/^\0plugin-vue/, '@vitejs/plugin-vue'],
  [/^\0vite\//, 'vite'],
]

/** 读包目录的 package.json。读不了、没有名字都直接报错（D-055）：已经打包进产物的包不能因为读不出来就从清单里漏掉。 */
function readPkg(dir: string): Pkg {
  let p: { name?: string; version?: string; license?: unknown }
  try {
    p = JSON.parse(readFileSync(join(dir, 'package.json'), 'utf8')) as typeof p
  } catch (e) {
    throw new Error(`cannot read ${join(dir, 'package.json')}: ${(e as Error).message}`, { cause: e })
  }
  if (!p.name) throw new Error(`${join(dir, 'package.json')} has no name`)
  const license = typeof p.license === 'string' ? p.license : 'UNKNOWN'
  return { name: p.name, version: p.version ?? '', license, dir }
}

/** 包目录里的许可证类文件：LICENSE、LICENCE、COPYING、UNLICENSE、NOTICE（不区分大小写，可带扩展名），NOTICE 排在后面。 */
function noticeFiles(dir: string): string[] {
  try {
    return readdirSync(dir)
      .filter((f) => /^(licen[cs]e|copying|unlicense|notice)(\.|-|$)/i.test(f))
      .sort((a, b) => (/notice/i.test(a) ? 1 : 0) - (/notice/i.test(b) ? 1 : 0) || a.localeCompare(b))
  } catch {
    return []
  }
}

/** 是否带许可证正文：只有 NOTICE 不算（D-055）。 */
function hasLicense(dir: string): boolean {
  return noticeFiles(dir).some((f) => !/^notice/i.test(f))
}

export function thirdPartyLicenses(opts: { fileName?: string; root?: string } = {}): Plugin {
  const fileName = opts.fileName ?? 'third-party-licenses.txt'
  return {
    name: 'ga-third-party-licenses',
    apply: 'build',
    generateBundle(_options, bundle) {
      const req = createRequire(join(opts.root ?? process.cwd(), 'package.json'))
      const dirs = new Set<string>()
      for (const out of Object.values(bundle)) {
        if (out.type !== 'chunk') continue
        for (const id of Object.keys(out.modules)) {
          const dir = packageDirOf(id)
          if (dir) {
            dirs.add(dir)
            continue
          }
          for (const [re, name] of VIRTUAL) {
            if (!re.test(id)) continue
            try {
              dirs.add(dirname(req.resolve(`${name}/package.json`)))
            } catch {
              // 找不到就算了：虚拟模块只是几行辅助代码
            }
          }
        }
      }
      const pkgs = new Map<string, Pkg>()
      for (const dir of dirs) {
        let p: Pkg
        try {
          p = readPkg(dir)
        } catch (e) {
          this.error((e as Error).message)
        }
        if (!p.name.startsWith('@ga/')) pkgs.set(`${p.name}@${p.version}`, p)
      }
      const list = [...pkgs.values()].sort((a, b) => a.name.localeCompare(b.name))
      const missing = list.filter((p) => !hasLicense(p.dir))
      if (missing.length) {
        this.error(`these bundled packages ship no license file; check their licence before depending on them: ${missing.map((p) => `${p.name}@${p.version}`).join(', ')}`)
      }
      const parts = [
        'Third-party software included in this build',
        '============================================',
        '',
        ...list.map((p) => `- ${p.name} ${p.version} (${p.license})`),
        '',
      ]
      for (const p of list) {
        parts.push('-'.repeat(72), `${p.name} ${p.version}`, `License: ${p.license}`, '-'.repeat(72))
        for (const f of noticeFiles(p.dir)) {
          const path = join(p.dir, f)
          if (existsSync(path)) parts.push(`[${f}]`, readFileSync(path, 'utf8').trim(), '')
        }
        parts.push('')
      }
      this.emitFile({ type: 'asset', fileName, source: parts.join('\n') })
    },
  }
}
