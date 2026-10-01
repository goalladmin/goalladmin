<script setup lang="ts">
// GoAllAdmin 的标识（透明底的图形部分）。同一张图也在仓库 docs/assets/ 下，README 和前端用的是同一个来源。
import logoMark from '../assets/logo-mark.png'

withDefaults(defineProps<{ size?: number }>(), { size: 32 })
</script>

<template>
  <img :src="logoMark" :width="size" :height="size" alt="GoAllAdmin" class="ga-logo" draggable="false" />
</template>

<style scoped>
.ga-logo {
  display: block;
  flex: 0 0 auto;
  object-fit: contain;
  user-select: none;
}
</style>
