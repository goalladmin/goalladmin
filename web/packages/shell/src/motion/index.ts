// 动效用的纯函数（D-030）：数字跳动的插值、迷你图的几何、"减少动态效果"的判断。组件只负责按帧调用它们。

/** 系统设置了"减少动态效果"时为 true：数字直接显示终值，图形不做入场动画。 */
export function prefersReducedMotion(): boolean {
  return typeof window !== 'undefined' && !!window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
}

/** 先快后慢的缓动（t 取 0–1）。 */
export function easeOutCubic(t: number): number {
  const x = Math.min(1, Math.max(0, t))
  return 1 - (1 - x) ** 3
}

/** 数字跳动的一帧：从 from 缓动到 to，进度 t（0–1）。t≥1 时正好是 to。 */
export function countFrame(from: number, to: number, t: number): number {
  if (t >= 1) return to
  return from + (to - from) * easeOutCubic(t)
}

export interface SparkPoint {
  x: number
  y: number
  v: number
}

export interface SparkGeometry {
  points: SparkPoint[]
  /** 折线的 path（M…L…）。 */
  line: string
  /** 折线下方填充区域的 path（闭合到底边）。 */
  area: string
  /** 柱子：左上角坐标和宽高。 */
  bars: { x: number; y: number; w: number; h: number; v: number }[]
}

/**
 * 按宽高算迷你图的几何。纵轴从 0 开始（不是从最小值开始，避免把小波动放大成大起伏）；
 * 全是 0 或没有数据时画一条贴底的线。pad 是上下留白。
 */
export function sparkGeometry(data: readonly number[], w: number, h: number, pad = 2): SparkGeometry {
  const vals = data.map((v) => (Number.isFinite(v) && v > 0 ? v : 0))
  const n = vals.length
  const maxV = Math.max(0, ...vals)
  const y = (v: number) => (maxV === 0 ? h - pad : h - pad - (v / maxV) * (h - 2 * pad))
  const points: SparkPoint[] =
    n === 0 ? [] : n === 1 ? [{ x: w / 2, y: y(vals[0]!), v: vals[0]! }] : vals.map((v, i) => ({ x: (i / (n - 1)) * w, y: y(v), v }))
  const r = (x: number) => Math.round(x * 100) / 100
  const line = points.map((p, i) => `${i ? 'L' : 'M'}${r(p.x)},${r(p.y)}`).join('')
  const area = points.length ? `${line}L${r(points[points.length - 1]!.x)},${h}L${r(points[0]!.x)},${h}Z` : ''
  const slot = n ? w / n : 0
  const bw = slot * 0.6
  const bars = vals.map((v, i) => {
    const top = y(v)
    // 0 不画柱子；再小的正数也至少 1 高，看得出有值
    return { x: r(i * slot + (slot - bw) / 2), y: r(v > 0 ? Math.min(top, h - 1) : h), w: r(bw), h: r(v > 0 ? Math.max(h - top, 1) : 0), v }
  })
  return { points, line, area, bars }
}

/** 正弦波浪的 path（水球用）：宽 w 的两个周期，振幅 amp，基线在 y=0，下方闭合到 depth。 */
export function wavePath(w: number, amp: number, depth: number, steps = 40): string {
  const pts: string[] = []
  for (let i = 0; i <= steps; i++) {
    const x = (i / steps) * w * 2
    const y = Math.sin((i / steps) * Math.PI * 4) * amp
    pts.push(`${i ? 'L' : 'M'}${Math.round(x * 100) / 100},${Math.round(y * 100) / 100}`)
  }
  return `${pts.join('')}L${w * 2},${depth}L0,${depth}Z`
}
