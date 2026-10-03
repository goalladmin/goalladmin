// ECharts 按需引入（D-027、D-030）：只注册控制台用到的图表和组件，打进控制台页面自己的分块，别的页面不加载。
// ECharts 是 Apache-2.0 许可，许可证和 NOTICE 随构建产物的 third-party-licenses.txt 分发。

import { BarChart, GaugeChart, LineChart, PieChart } from 'echarts/charts'
import { GridComponent, LegendComponent, TooltipComponent } from 'echarts/components'
import { init, use } from 'echarts/core'
import type { EChartsCoreOption, EChartsType } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'

use([LineChart, BarChart, PieChart, GaugeChart, GridComponent, TooltipComponent, LegendComponent, CanvasRenderer])

export { init }
export type { EChartsCoreOption, EChartsType }

/**
 * 系列颜色：按固定顺序分配、不循环；亮色和暗色各一组（同一组色相，暗色为暗底调整过），
 * 都用校验脚本检查过色觉障碍下的区分度。文字一律用文字色，不用系列色。
 */
export const SeriesColors = {
  light: ['#2a78d6', '#eb6834', '#1baf7a', '#eda100', '#e87ba4', '#008300'],
  dark: ['#3987e5', '#d95926', '#199e70', '#c98500', '#d55181', '#008300'],
}

/** 从页面的 CSS 变量取文字、边框和底色，让图表跟着亮暗主题走。 */
export function chartTheme(dark: boolean) {
  const css = typeof document !== 'undefined' ? getComputedStyle(document.documentElement) : null
  const v = (name: string, def: string) => css?.getPropertyValue(name).trim() || def
  return {
    colors: dark ? SeriesColors.dark : SeriesColors.light,
    text: v('--ga-text', dark ? '#e2e8f0' : '#1e293b'),
    muted: v('--ga-text-secondary', dark ? '#94a3b8' : '#64748b'),
    border: v('--ga-border', dark ? '#243049' : '#e6e9ef'),
    surface: v('--ga-surface', dark ? '#141a26' : '#ffffff'),
  }
}

/** 动画的统一设置：入场约 1 秒、先快后慢；数据更新时 0.6 秒过渡。 */
export const ChartMotion = {
  animationDuration: 1000,
  animationEasing: 'cubicOut',
  animationDurationUpdate: 600,
  animationEasingUpdate: 'cubicOut',
} as const

/** 坐标轴的公共样式：轴线和网格线用边框色、刻度文字用次要文字色。 */
export function axisStyle(th: ReturnType<typeof chartTheme>) {
  return {
    axisLine: { lineStyle: { color: th.border } },
    axisTick: { show: false },
    axisLabel: { color: th.muted },
    splitLine: { lineStyle: { color: th.border, type: 'dashed' } },
  }
}

/** 提示框的公共样式。 */
export function tooltipStyle(th: ReturnType<typeof chartTheme>, trigger: 'axis' | 'item') {
  return {
    trigger,
    backgroundColor: th.surface,
    borderColor: th.border,
    textStyle: { color: th.text },
    axisPointer: trigger === 'axis' ? { type: 'line', lineStyle: { color: th.muted } } : undefined,
  }
}
