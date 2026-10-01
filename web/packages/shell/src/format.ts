// 按界面语言格式化数字、字节数、相对时间（D-030 控制台用）。都用浏览器自带的 Intl，不需要翻译文案。

/** 字节数：按 1024 进位到 B / KB / MB / GB / TB，保留 1 位小数（整数时不带小数）。 */
export function formatBytes(bytes: number | null | undefined, locale: string): string {
  const units = ['byte', 'kilobyte', 'megabyte', 'gigabyte', 'terabyte'] as const
  let v = Number(bytes)
  if (!Number.isFinite(v) || v < 0) v = 0
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return new Intl.NumberFormat(locale, { style: 'unit', unit: units[i], unitDisplay: 'short', maximumFractionDigits: i === 0 ? 0 : 1 }).format(v)
}

/** 相对时间："刚刚 / 3 分钟前 / 2 小时前 / 昨天 / 5 天前"；超过 30 天显示日期。 */
export function formatRelative(v: string | Date | null | undefined, locale: string, now: Date = new Date()): string {
  if (!v) return ''
  const d = v instanceof Date ? v : new Date(v)
  if (Number.isNaN(d.getTime())) return String(v)
  const sec = Math.round((d.getTime() - now.getTime()) / 1000)
  const abs = Math.abs(sec)
  const rtf = new Intl.RelativeTimeFormat(locale, { numeric: 'auto' })
  if (abs < 45) return rtf.format(0, 'second')
  if (abs < 45 * 60) return rtf.format(Math.round(sec / 60), 'minute')
  if (abs < 22 * 3600) return rtf.format(Math.round(sec / 3600), 'hour')
  if (abs < 30 * 86400) return rtf.format(Math.round(sec / 86400), 'day')
  return new Intl.DateTimeFormat(locale, { year: 'numeric', month: 'short', day: 'numeric' }).format(d)
}

/** 时长（秒）拆成 天、小时、分钟、秒，页面按语言拼成文字。 */
export function splitDuration(seconds: number): { d: number; h: number; m: number; s: number } {
  let v = Math.max(0, Math.floor(Number.isFinite(seconds) ? seconds : 0))
  const d = Math.floor(v / 86400)
  v -= d * 86400
  const h = Math.floor(v / 3600)
  v -= h * 3600
  const m = Math.floor(v / 60)
  return { d, h, m, s: v - m * 60 }
}
