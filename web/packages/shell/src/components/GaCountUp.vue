<script setup lang="ts">
// 数字跳动（D-030）：挂载时从 0 缓动到目标值，值变化时从当前显示值缓动到新值；
// 被缓存的页面重新进入时再从 0 跳一次。系统设置了"减少动态效果"时直接显示终值。
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import { countFrame, prefersReducedMotion } from '../motion'

const props = withDefaults(
  defineProps<{
    value: number | null | undefined
    /** 动画时长（毫秒）。 */
    duration?: number
    /** 小数位数。 */
    decimals?: number
    suffix?: string
  }>(),
  { duration: 1200, decimals: 0, suffix: '' },
)

const { locale } = useI18n()
const shown = ref(0)
let raf = 0

function target(): number {
  const v = Number(props.value)
  return Number.isFinite(v) ? v : 0
}

function run(from: number) {
  const to = target()
  if (typeof requestAnimationFrame === 'undefined' || prefersReducedMotion() || props.duration <= 0 || from === to) {
    shown.value = to
    return
  }
  cancelAnimationFrame(raf)
  const t0 = performance.now()
  const step = (now: number) => {
    const t = (now - t0) / props.duration
    shown.value = countFrame(from, to, t)
    if (t < 1) raf = requestAnimationFrame(step)
  }
  raf = requestAnimationFrame(step)
}

watch(
  () => props.value,
  () => run(shown.value),
)
onMounted(() => run(0))
let away = false
onDeactivated(() => (away = true))
onActivated(() => {
  if (away) {
    away = false
    run(0)
  }
})
onBeforeUnmount(() => {
  if (typeof cancelAnimationFrame !== 'undefined') cancelAnimationFrame(raf)
})

const text = computed(() => {
  const f = new Intl.NumberFormat(locale.value, { minimumFractionDigits: props.decimals, maximumFractionDigits: props.decimals })
  return f.format(shown.value) + props.suffix
})
</script>

<template>
  <span class="ga-count-up" :data-value="target()">{{ text }}</span>
</template>

<style>
.ga-count-up {
  font-variant-numeric: tabular-nums;
}
</style>
