// 头像（D-040）：只认三种值，内置头像直接生成，别的写法（外部地址等）一律当作没有头像。

import { describe, expect, it } from 'vitest'

import { AVATAR_COLORS, AVATAR_PRESETS, avatarColor, avatarInitial, parseAvatar, presetAvatarSrc } from './avatar'

describe('头像', () => {
  it('只认空、内置和上传三种值，外部地址和乱写的一律当作没有头像', () => {
    expect(parseAvatar('')).toEqual({ kind: 'none' })
    expect(parseAvatar(null)).toEqual({ kind: 'none' })
    expect(parseAvatar('preset:ocean')).toEqual({ kind: 'preset', name: 'ocean' })
    expect(parseAvatar('upload:' + 'a'.repeat(32))).toEqual({ kind: 'upload', key: 'a'.repeat(32) })
    for (const bad of ['https://tracker.example/p.png', 'preset:evil', 'upload:../x', 'upload:' + 'A'.repeat(32), 'javascript:alert(1)', 'data:image/svg+xml,<svg onload=alert(1)>']) {
      expect(parseAvatar(bad)).toEqual({ kind: 'none' })
    }
  })

  it('12 个内置头像，名字不重复，都能生成 SVG', () => {
    expect(AVATAR_PRESETS).toHaveLength(12)
    expect(new Set(AVATAR_PRESETS.map((p) => p.name)).size).toBe(12)
    for (const p of AVATAR_PRESETS) {
      const src = presetAvatarSrc(p.name)!
      expect(src.startsWith('data:image/svg+xml')).toBe(true)
      const svg = decodeURIComponent(src.split(',')[1]!)
      expect(svg).not.toMatch(/<script|on\w+=|href/i)
    }
    expect(presetAvatarSrc('nope')).toBeUndefined()
  })

  it('首字母', () => {
    expect(avatarInitial('bob')).toBe('B')
    expect(avatarInitial('张三')).toBe('张')
    expect(avatarInitial('  ')).toBe('?')
  })

  it('首字母圆圈的配色：100 组、20 个色系（含灰色系），底色不黑不白，文字和底色对比度 ≥ 4.5', () => {
    const lum = (hex: string) => {
      const c = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255).map((v) => (v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4))
      return 0.2126 * c[0]! + 0.7152 * c[1]! + 0.0722 * c[2]!
    }
    const contrast = (a: string, b: string) => (Math.max(lum(a), lum(b)) + 0.05) / (Math.min(lum(a), lum(b)) + 0.05)
    expect(AVATAR_COLORS).toHaveLength(100)
    expect(new Set(AVATAR_COLORS.map(([bg]) => bg)).size).toBe(100)
    for (const [bg, fg] of AVATAR_COLORS) {
      expect(bg).toMatch(/^#[0-9a-f]{6}$/)
      expect(fg).toMatch(/^#[0-9a-f]{6}$/)
      expect(contrast(bg, fg)).toBeGreaterThanOrEqual(4.5)
      expect(lum(bg)).toBeGreaterThan(0.05) // 不是黑色
      expect(lum(bg)).toBeLessThan(0.8) // 不是白色
    }
    // 有灰色系：几乎没有彩度的底色至少 10 个
    const gray = AVATAR_COLORS.filter(([bg]) => {
      const [r, g, b] = [1, 3, 5].map((i) => parseInt(bg.slice(i, i + 2), 16))
      return Math.max(r!, g!, b!) - Math.min(r!, g!, b!) < 40
    })
    expect(gray.length).toBeGreaterThanOrEqual(10)
  })

  it('同一个账号总是同一种颜色，不同账号分散开', () => {
    expect(avatarColor('alice')).toEqual(avatarColor('alice'))
    const used = new Set(Array.from({ length: 300 }, (_, i) => avatarColor(`user${i}`).bg))
    expect(used.size).toBeGreaterThan(80)
    expect(AVATAR_COLORS.map(([bg]) => bg)).toContain(avatarColor('').bg)
  })
})
