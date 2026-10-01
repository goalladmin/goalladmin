// 动效（D-030）：缓动与插值、迷你图几何、数字跳动组件（按帧推进、减少动态效果时直接显示终值）。

import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import { createI18n } from 'vue-i18n'

import GaCountUp from '../components/GaCountUp.vue'
import { countFrame, easeOutCubic, sparkGeometry, wavePath } from './index'

describe('缓动', () => {
  it('端点准确、单调递增、先快后慢', () => {
    expect(easeOutCubic(0)).toBe(0)
    expect(easeOutCubic(1)).toBe(1)
    expect(easeOutCubic(-1)).toBe(0)
    expect(easeOutCubic(2)).toBe(1)
    let prev = 0
    for (let i = 1; i <= 10; i++) {
      const v = easeOutCubic(i / 10)
      expect(v).toBeGreaterThan(prev)
      prev = v
    }
    expect(easeOutCubic(0.5)).toBeGreaterThan(0.5)
  })

  it('countFrame 从起点到终点，结束时正好是终值', () => {
    expect(countFrame(0, 100, 0)).toBe(0)
    expect(countFrame(0, 100, 1)).toBe(100)
    expect(countFrame(0, 100, 5)).toBe(100)
    expect(countFrame(100, 40, 1)).toBe(40)
    const mid = countFrame(100, 40, 0.5)
    expect(mid).toBeLessThan(100)
    expect(mid).toBeGreaterThan(40)
  })
})

describe('sparkGeometry', () => {
  it('纵轴从 0 开始，最大值贴顶、0 贴底', () => {
    const g = sparkGeometry([0, 5, 10], 100, 50, 2)
    expect(g.points.map((p) => p.x)).toEqual([0, 50, 100])
    expect(g.points[0]!.y).toBe(48)
    expect(g.points[2]!.y).toBe(2)
    expect(g.points[1]!.y).toBe(25)
    expect(g.line.startsWith('M0,48')).toBe(true)
    expect(g.area.endsWith('Z')).toBe(true)
  })

  it('全是 0、负数、NaN 或空：不出错，画贴底的线', () => {
    expect(sparkGeometry([], 100, 40).line).toBe('')
    expect(sparkGeometry([], 100, 40).area).toBe('')
    const g = sparkGeometry([0, -3, Number.NaN], 100, 40, 2)
    expect(g.points.every((p) => p.y === 38)).toBe(true)
    expect(g.bars.every((b) => b.h === 0)).toBe(true)
    const one = sparkGeometry([7], 100, 40)
    expect(one.points).toHaveLength(1)
    expect(one.points[0]!.x).toBe(50)
  })

  it('柱子等宽、落在各自的格子里，非零的柱子至少 1 高', () => {
    const g = sparkGeometry([1, 1000], 100, 40, 2)
    expect(g.bars).toHaveLength(2)
    expect(g.bars[0]!.w).toBe(g.bars[1]!.w)
    expect(g.bars[0]!.x).toBeLessThan(50)
    expect(g.bars[1]!.x).toBeGreaterThanOrEqual(50)
    expect(g.bars[0]!.h).toBeGreaterThanOrEqual(1)
  })

  it('wavePath 闭合', () => {
    const p = wavePath(100, 3, 120)
    expect(p.startsWith('M0,0')).toBe(true)
    expect(p.endsWith('Z')).toBe(true)
  })
})

describe('GaCountUp', () => {
  const i18n = () => createI18n({ legacy: false, locale: 'en-US', messages: { 'en-US': {} } })
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  it('按帧从 0 跳到目标值，结束时显示终值；值变化时从当前值继续', async () => {
    const frames: FrameRequestCallback[] = []
    vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback) => frames.push(cb))
    vi.stubGlobal('cancelAnimationFrame', () => {})
    let now = 0
    vi.spyOn(performance, 'now').mockImplementation(() => now)
    const w = mount(GaCountUp, { props: { value: 1000, duration: 1000 }, global: { plugins: [i18n()] } })
    expect(w.text()).toBe('0')
    frames.shift()!(500)
    await nextTick()
    const mid = Number(w.text().replace(/,/g, ''))
    expect(mid).toBeGreaterThan(500)
    expect(mid).toBeLessThan(1000)
    frames.shift()!(1000)
    await nextTick()
    expect(w.text()).toBe('1,000')
    expect(frames).toHaveLength(0)

    now = 1000
    await w.setProps({ value: 2000 })
    frames.shift()!(2000)
    await nextTick()
    expect(w.text()).toBe('2,000')
  })

  it('减少动态效果时直接显示终值，带小数和后缀', async () => {
    vi.stubGlobal('matchMedia', (q: string) => ({ matches: q.includes('reduce'), media: q }))
    const w = mount(GaCountUp, { props: { value: 12.345, decimals: 1, suffix: '%' }, global: { plugins: [i18n()] } })
    await nextTick()
    expect(w.text()).toBe('12.3%')
  })

  it('非数字按 0 处理', () => {
    vi.stubGlobal('matchMedia', () => ({ matches: true }))
    const w = mount(GaCountUp, { props: { value: null }, global: { plugins: [i18n()] } })
    expect(w.text()).toBe('0')
  })
})
