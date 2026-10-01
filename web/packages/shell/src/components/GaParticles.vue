<script setup lang="ts">
// 登录页背景：一片缓慢漂移的粒子，相近的两点之间连线，鼠标附近的点会被轻轻牵引。
// 纯 canvas，不依赖第三方库；系统开了"减少动态效果"时只画一帧静态图；页面不可见时暂停。
import { onBeforeUnmount, onMounted, ref } from 'vue'

const canvas = ref<HTMLCanvasElement>()

type Particle = { x: number; y: number; vx: number; vy: number; r: number; hue: number }

let ctx: CanvasRenderingContext2D | null = null
let raf = 0
let width = 0
let height = 0
let particles: Particle[] = []
const mouse = { x: -1e4, y: -1e4 }
const LINK = 140
const reduced = typeof window !== 'undefined' && !!window.matchMedia?.('(prefers-reduced-motion: reduce)').matches

function resize() {
  const el = canvas.value
  if (!el || !ctx) return
  const dpr = Math.min(window.devicePixelRatio || 1, 2)
  width = el.clientWidth
  height = el.clientHeight
  el.width = Math.floor(width * dpr)
  el.height = Math.floor(height * dpr)
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
  const n = Math.max(40, Math.min(120, Math.floor((width * height) / 14000)))
  particles = Array.from({ length: n }, () => ({
    x: Math.random() * width,
    y: Math.random() * height,
    vx: (Math.random() - 0.5) * 0.35,
    vy: (Math.random() - 0.5) * 0.35,
    r: 1 + Math.random() * 1.6,
    hue: Math.random() < 0.5 ? 210 : 185,
  }))
}

function draw() {
  if (!ctx) return
  ctx.clearRect(0, 0, width, height)
  for (let i = 0; i < particles.length; i++) {
    const p = particles[i]!
    for (let j = i + 1; j < particles.length; j++) {
      const q = particles[j]!
      const dx = p.x - q.x
      const dy = p.y - q.y
      const d = Math.hypot(dx, dy)
      if (d < LINK) {
        ctx.strokeStyle = `hsla(200, 90%, 70%, ${(1 - d / LINK) * 0.28})`
        ctx.lineWidth = 1
        ctx.beginPath()
        ctx.moveTo(p.x, p.y)
        ctx.lineTo(q.x, q.y)
        ctx.stroke()
      }
    }
    const dm = Math.hypot(p.x - mouse.x, p.y - mouse.y)
    if (dm < 180) {
      ctx.strokeStyle = `hsla(190, 100%, 75%, ${(1 - dm / 180) * 0.5})`
      ctx.beginPath()
      ctx.moveTo(p.x, p.y)
      ctx.lineTo(mouse.x, mouse.y)
      ctx.stroke()
    }
    ctx.fillStyle = `hsla(${p.hue}, 95%, 72%, 0.9)`
    ctx.beginPath()
    ctx.arc(p.x, p.y, p.r, 0, Math.PI * 2)
    ctx.fill()
  }
}

function step() {
  for (const p of particles) {
    const dm = Math.hypot(p.x - mouse.x, p.y - mouse.y)
    if (dm < 180 && dm > 0.001) {
      p.vx += ((mouse.x - p.x) / dm) * 0.004
      p.vy += ((mouse.y - p.y) / dm) * 0.004
    }
    p.vx = Math.max(-0.6, Math.min(0.6, p.vx))
    p.vy = Math.max(-0.6, Math.min(0.6, p.vy))
    p.x += p.vx
    p.y += p.vy
    if (p.x < -10) p.x = width + 10
    if (p.x > width + 10) p.x = -10
    if (p.y < -10) p.y = height + 10
    if (p.y > height + 10) p.y = -10
  }
  draw()
  raf = requestAnimationFrame(step)
}

function start() {
  if (raf || reduced) return
  raf = requestAnimationFrame(step)
}

function stop() {
  if (raf) cancelAnimationFrame(raf)
  raf = 0
}

function onMove(e: MouseEvent) {
  const rect = canvas.value?.getBoundingClientRect()
  if (!rect) return
  mouse.x = e.clientX - rect.left
  mouse.y = e.clientY - rect.top
}

function onLeave() {
  mouse.x = -1e4
  mouse.y = -1e4
}

function onVisibility() {
  if (document.hidden) stop()
  else start()
}

onMounted(() => {
  const el = canvas.value
  ctx = el?.getContext('2d') ?? null
  if (!el || !ctx) return
  resize()
  draw()
  window.addEventListener('resize', resize)
  window.addEventListener('mousemove', onMove, { passive: true })
  window.addEventListener('mouseleave', onLeave)
  document.addEventListener('visibilitychange', onVisibility)
  start()
})

onBeforeUnmount(() => {
  stop()
  window.removeEventListener('resize', resize)
  window.removeEventListener('mousemove', onMove)
  window.removeEventListener('mouseleave', onLeave)
  document.removeEventListener('visibilitychange', onVisibility)
})
</script>

<template>
  <canvas ref="canvas" class="ga-particles" aria-hidden="true" />
</template>

<style scoped>
.ga-particles {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  display: block;
}
</style>
