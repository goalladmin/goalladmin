<script setup lang="ts">
// 登录、改密两个页面共用的舞台：深色底、漂移的光斑、透视网格、粒子连线，中间一张玻璃卡片。
import GaLanguageSwitch from './GaLanguageSwitch.vue'
import GaParticles from './GaParticles.vue'
</script>

<template>
  <div class="ga-auth">
    <div class="ga-auth__bg" aria-hidden="true">
      <div class="ga-auth__orb ga-auth__orb--1" />
      <div class="ga-auth__orb ga-auth__orb--2" />
      <div class="ga-auth__orb ga-auth__orb--3" />
      <div class="ga-auth__grid" />
      <GaParticles />
    </div>
    <div class="ga-auth__lang">
      <GaLanguageSwitch show-label />
    </div>
    <div class="ga-auth__card">
      <slot />
    </div>
    <div class="ga-auth__foot">
      <slot name="foot" />
    </div>
  </div>
</template>
