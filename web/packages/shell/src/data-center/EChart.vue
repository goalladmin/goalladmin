<script setup lang="ts">
// 一个 ECharts 图表：随容器大小自动调整，组件卸载时释放。
// 入场时图形长出来（ECharts 自带动画）；被缓存的页面重新进入时重播一次（D-030）。
// live 为 true 时（定时刷新的图表）新数据在原图上平滑过渡，不重播入场动画。
import { onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'

import { init } from './echarts'
import type { EChartsCoreOption, EChartsType } from './echarts'

const props = defineProps<{ option: EChartsCoreOption; height?: string; live?: boolean }>()
const el = ref<HTMLDivElement>()
let chart: EChartsType | null = null
let ro: ResizeObserver | null = null

onMounted(() => {
  if (!el.value) return
  chart = init(el.value, undefined, { renderer: 'canvas' })
  chart.setOption(props.option, true)
  ro = new ResizeObserver(() => chart?.resize())
  ro.observe(el.value)
})

watch(
  () => props.option,
  (o) => chart?.setOption(o, { notMerge: !props.live }),
)

let away = false
onDeactivated(() => (away = true))
onActivated(() => {
  if (!away || !chart) return
  away = false
  chart.resize()
  chart.clear()
  chart.setOption(props.option, true)
})

onBeforeUnmount(() => {
  ro?.disconnect()
  chart?.dispose()
  chart = null
})
</script>

<template>
  <div ref="el" class="ga-echart" :style="{ height: height ?? '320px' }" />
</template>

<style scoped>
.ga-echart {
  width: 100%;
}
</style>
