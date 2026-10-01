<script setup lang="ts">
// 迷你趋势图（D-030）：卡片里用的小折线、面积或柱状图，纯 SVG，不依赖图表库。
// 入场时折线和面积从左往右展开（裁剪整张图，不依赖线条长度，图被横向拉伸时也准确），柱子从底部长高；
// 数据变化或页面重新进入时重播。
// 鼠标移到柱子或折线的点上显示数值（原生 title）。
import { computed, onActivated, onDeactivated, ref, useId, watch } from 'vue'

import { sparkGeometry } from '../motion'

const props = withDefaults(
  defineProps<{
    data: readonly number[]
    type?: 'line' | 'area' | 'bar'
    /** 颜色；默认主题色。 */
    color?: string
    height?: number
    /** 每个数据点的说明（和 data 对齐），用于悬停提示。 */
    labels?: readonly string[]
  }>(),
  { type: 'area', color: 'var(--el-color-primary)', height: 46, labels: () => [] },
)

const W = 100
const gid = `ga-spark-${useId()}`
const geo = computed(() => sparkGeometry(props.data, W, props.height))
const tip = (i: number, v: number) => (props.labels[i] ? `${props.labels[i]}: ${v}` : String(v))

// 改 key 让 SVG 重新创建，CSS 动画从头播放
const gen = ref(0)
watch(
  () => props.data.join(','),
  () => gen.value++,
)
let away = false
onDeactivated(() => (away = true))
onActivated(() => {
  if (away) {
    away = false
    gen.value++
  }
})
</script>

<template>
  <svg
    :key="gen"
    class="ga-spark"
    :class="`ga-spark--${props.type}`"
    :viewBox="`0 0 ${W} ${props.height}`"
    preserveAspectRatio="none"
    :style="{ height: `${props.height}px`, color: props.color }"
    role="img"
  >
    <defs>
      <linearGradient :id="gid" x1="0" y1="0" x2="0" y2="1">
        <stop offset="0%" stop-color="currentColor" stop-opacity="0.35" />
        <stop offset="100%" stop-color="currentColor" stop-opacity="0.02" />
      </linearGradient>
    </defs>
    <template v-if="props.type === 'bar'">
      <rect
        v-for="(b, i) in geo.bars"
        :key="i"
        class="ga-spark__bar"
        :x="b.x"
        :y="b.y"
        :width="b.w"
        :height="b.h"
        rx="0.8"
        fill="currentColor"
        :style="{ animationDelay: `${i * 25}ms` }"
      >
        <title>{{ tip(i, b.v) }}</title>
      </rect>
    </template>
    <template v-else>
      <path v-if="props.type === 'area'" class="ga-spark__area" :d="geo.area" :fill="`url(#${gid})`" />
      <path class="ga-spark__line" :d="geo.line" fill="none" stroke="currentColor" stroke-width="2" vector-effect="non-scaling-stroke" stroke-linejoin="round" stroke-linecap="round" />
      <rect v-for="(p, i) in geo.points" :key="i" class="ga-spark__hit" :x="p.x - W / geo.points.length / 2" y="0" :width="W / Math.max(geo.points.length, 1)" :height="props.height" fill="transparent">
        <title>{{ tip(i, p.v) }}</title>
      </rect>
    </template>
  </svg>
</template>

<style>
.ga-spark {
  display: block;
  width: 100%;
  overflow: visible;
}
.ga-spark--line,
.ga-spark--area {
  animation: ga-spark-reveal 1.1s cubic-bezier(0.3, 0.7, 0.2, 1) both;
}
.ga-spark__area {
  animation: ga-spark-fade 0.9s ease-out 0.3s both;
}
.ga-spark__bar {
  transform-box: fill-box;
  transform-origin: 50% 100%;
  animation: ga-spark-grow 0.6s cubic-bezier(0.2, 0.8, 0.2, 1) both;
}
.ga-spark__bar:hover {
  opacity: 0.75;
}
@keyframes ga-spark-reveal {
  from {
    clip-path: inset(-4px 100% -4px 0);
  }
  to {
    clip-path: inset(-4px 0 -4px 0);
  }
}
@keyframes ga-spark-fade {
  from {
    opacity: 0;
  }
}
@keyframes ga-spark-grow {
  from {
    transform: scaleY(0);
  }
}
@media (prefers-reduced-motion: reduce) {
  .ga-spark--line,
  .ga-spark--area,
  .ga-spark__area,
  .ga-spark__bar {
    animation: none;
  }
}
</style>
