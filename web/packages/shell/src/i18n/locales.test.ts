// 文案文件的一致性（D-026）：壳和各端的每个 locales 目录里，每种语言的文件与 zh-CN.ts 的键完全一致、
// 占位符一致、不含会被 vue-i18n 当作语法的字符。加一种语言或加一个键时，漏翻、多翻、写错占位符都会在这里失败。

import { describe, expect, it } from 'vitest'

import { AllLocaleCodes } from './languages'

type Tree = Record<string, unknown>

const shell = import.meta.glob<{ default: Tree }>('./*-*.ts', { eager: true })
const apps = import.meta.glob<{ default: Tree }>('../../../../apps/*/src/**/locales/*.ts', { eager: true })

/** 按目录分组：目录 → { 语言代码: 文案 }。 */
function groups(): Map<string, Map<string, Tree>> {
  const out = new Map<string, Map<string, Tree>>()
  for (const [path, mod] of Object.entries({ ...shell, ...apps })) {
    const file = path.split('/').pop() ?? ''
    const code = file.replace(/\.ts$/, '')
    if (!(AllLocaleCodes as readonly string[]).includes(code)) continue
    const dir = path.slice(0, path.length - file.length)
    if (!out.has(dir)) out.set(dir, new Map())
    out.get(dir)!.set(code, mod.default)
  }
  return out
}

function flatten(t: Tree, prefix = '', out = new Map<string, string>()): Map<string, string> {
  for (const [k, v] of Object.entries(t)) {
    const key = prefix ? `${prefix}\u0000${k}` : k
    if (v && typeof v === 'object' && !Array.isArray(v)) flatten(v as Tree, key, out)
    else out.set(key, String(v))
  }
  return out
}

const placeholders = (s: string) => [...s.matchAll(/\{([A-Za-z_][A-Za-z0-9_]*)\}/g)].map((m) => m[1]).sort()
const show = (k: string) => k.split('\u0000').join('.')

describe('文案文件', () => {
  const all = groups()

  it('找到了壳和平台端的文案目录', () => {
    expect(all.size).toBeGreaterThanOrEqual(2)
  })

  for (const [dir, byLang] of all) {
    describe(dir, () => {
      const ref = byLang.get('zh-CN')
      it('有 zh-CN.ts 作为基准', () => expect(ref).toBeDefined())
      if (!ref) return
      const refKeys = flatten(ref)

      for (const code of AllLocaleCodes) {
        it(`${code}：键与 zh-CN 一致、占位符一致、没有文案语法字符`, () => {
          const cur = byLang.get(code)
          expect(cur, `${dir}${code}.ts 不存在`).toBeDefined()
          const keys = flatten(cur!)
          const missing = [...refKeys.keys()].filter((k) => !keys.has(k)).map(show)
          const extra = [...keys.keys()].filter((k) => !refKeys.has(k)).map(show)
          expect(missing, '缺少的键').toEqual([])
          expect(extra, '多出的键').toEqual([])
          const problems: string[] = []
          for (const [k, v] of keys) {
            if (!v.trim()) problems.push(`${show(k)}：空文案`)
            if (/[@|$]/.test(v)) problems.push(`${show(k)}：含 @ | $（vue-i18n 语法字符）`)
            const stray = v.replace(/\{[A-Za-z_][A-Za-z0-9_]*\}/g, '')
            if (/[{}]/.test(stray)) problems.push(`${show(k)}：花括号只能用于占位符`)
            if (placeholders(v).join() !== placeholders(refKeys.get(k) ?? '').join()) {
              problems.push(`${show(k)}：占位符 ${placeholders(v).join(',')} 与 zh-CN 的 ${placeholders(refKeys.get(k) ?? '').join(',')} 不一致`)
            }
          }
          expect(problems).toEqual([])
        })
      }
    })
  }
})
