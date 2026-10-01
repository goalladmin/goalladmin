// 主题色（D-027）：由一个主色算出 Element Plus 需要的浅色、深色梯度，写到 <html> 的 CSS 变量上。

/** #RRGGBB → [r, g, b]；不合法时返回 null。 */
export function parseHex(hex: string): [number, number, number] | null {
  const m = /^#([0-9a-fA-F]{6})$/.exec(hex)
  if (!m) return null
  const n = parseInt(m[1]!, 16)
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255]
}

function toHex(rgb: [number, number, number]): string {
  return `#${rgb.map((c) => Math.round(Math.min(255, Math.max(0, c))).toString(16).padStart(2, '0')).join('')}`
}

/** 按 weight（0–1，混入 b 的比例）混合两种颜色。 */
export function mix(a: string, b: string, weight: number): string {
  const x = parseHex(a)
  const y = parseHex(b)
  if (!x || !y) return a
  return toHex([0, 1, 2].map((i) => x[i]! * (1 - weight) + y[i]! * weight) as [number, number, number])
}

/** 主色的全部梯度：亮色下向白色混，暗色下向页面底色混。 */
export function primaryShades(primary: string, dark: boolean): Record<string, string> {
  const base = dark ? '#141a26' : '#ffffff'
  const out: Record<string, string> = {
    '--ga-primary': primary,
    '--el-color-primary': primary,
    '--el-color-primary-dark-2': mix(primary, dark ? '#ffffff' : '#000000', 0.2),
  }
  for (const l of [3, 5, 7, 8, 9]) out[`--el-color-primary-light-${l}`] = mix(primary, base, l / 10)
  return out
}

export function applyPrimaryColor(primary: string, dark: boolean): void {
  if (typeof document === 'undefined' || !parseHex(primary)) return
  const style = document.documentElement.style
  for (const [k, v] of Object.entries(primaryShades(primary, dark))) style.setProperty(k, v)
}
