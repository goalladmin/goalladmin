<script setup lang="ts">
// 水球图（D-030）：圆里的水位表示占比，水面是两层错开流动的波浪；水位变化时平滑升降，中间的数字跳动。
// 纯 SVG + CSS。系统设置了"减少动态效果"时波浪不动。
import { computed, useId } from 'vue'

import GaCountUp from './GaCountUp.vue'
import { wavePath } from '../motion'

const props = withDefaults(
  defineProps<{
    /** 0–100。 */
    percent: number
    label?: string
    size?: number
    color?: string
  }>(),
  { label: '', size: 160, color: 'var(--el-color-primary)' },
)

const cid = `ga-liquid-${useId()}`
const pct = computed(() => Math.min(100, Math.max(0, Number.isFinite(props.percent) ? props.percent : 0)))
// 圆的可见范围是 y=4..96；水位线的 y 坐标
const level = computed(() => 96 - (pct.value / 100) * 92)
const wave = wavePath(100, 3, 120)
</script>

<template>
  <div class="ga-liquid" :style="{ width: `${props.size}px`, height: `${props.size}px`, color: props.color }">
    <svg viewBox="0 0 100 100" aria-hidden="true">
      <defs>
        <clipPath :id="cid">
          <circle cx="50" cy="50" r="46" />
        </clipPath>
      </defs>
      <circle cx="50" cy="50" r="48.5" fill="none" stroke="currentColor" stroke-width="1.5" opacity="0.5" />
      <circle cx="50" cy="50" r="46" class="ga-liquid__bg" />
      <g :clip-path="`url(#${cid})`">
        <g class="ga-liquid__level" :style="{ transform: `translateY(${level}px)` }">
          <path class="ga-liquid__wave ga-liquid__wave--back" :d="wave" fill="currentColor" opacity="0.35" />
          <path class="ga-liquid__wave" :d="wave" fill="currentColor" opacity="0.85" />
        </g>
      </g>
    </svg>
    <div class="ga-liquid__text">
      <GaCountUp :value="pct" :decimals="pct > 0 && pct < 10 ? 1 : 0" suffix="%" class="ga-liquid__value" />
      <span v-if="props.label" class="ga-liquid__label">{{ props.label }}</span>
    </div>
  </div>
</template>

<style>
.ga-liquid {
  position: relative;
  flex: none;
}
.ga-liquid svg {
  display: block;
  width: 100%;
  height: 100%;
}
.ga-liquid__bg {
  fill: var(--el-fill-color-lighter);
}
.ga-liquid__level {
  transition: transform 1.2s cubic-bezier(0.2, 0.8, 0.2, 1);
}
.ga-liquid__wave {
  animation: ga-liquid-flow 3.2s linear infinite;
}
.ga-liquid__wave--back {
  animation-duration: 4.6s;
  animation-direction: reverse;
}
@keyframes ga-liquid-flow {
  from {
    transform: translateX(0);
  }
  to {
    transform: translateX(-100px);
  }
}
.ga-liquid__text {
  position: absolute;
  inset: 0;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 2px;
  color: var(--el-text-color-primary);
  text-shadow:
    0 0 4px var(--el-bg-color),
    0 0 2px var(--el-bg-color);
  pointer-events: none;
}
.ga-liquid__value {
  font-size: 26px;
  font-weight: 600;
}
.ga-liquid__label {
  font-size: 12px;
  color: var(--el-text-color-regular);
}
@media (prefers-reduced-motion: reduce) {
  .ga-liquid__wave {
    animation: none;
  }
  .ga-liquid__level {
    transition: none;
  }
}
</style>
