<script setup lang="ts">
// 语言的旗帜（D-026）：SVG 图片，Windows 上也能显示；没有旗帜的语言（繁体中文）显示字标。
import { computed } from 'vue'

import { languageInfo } from '../i18n/languages'
import type { LocaleCode } from '../i18n/languages'

// decorative：旁边已经写了语言名时为 true，图片不再重复朗读
const props = withDefaults(defineProps<{ code: LocaleCode; decorative?: boolean }>(), { decorative: false })
const info = computed(() => languageInfo(props.code))
</script>

<template>
  <img v-if="info.flag" class="ga-flag" :src="info.flag" :alt="decorative ? '' : info.label" draggable="false" />
  <span v-else class="ga-flag ga-flag--badge" :aria-hidden="decorative ? 'true' : undefined" :aria-label="decorative ? undefined : info.label">{{ info.badge }}</span>
</template>

<style scoped>
.ga-flag {
  display: inline-block;
  width: 20px;
  height: 15px;
  border-radius: 2px;
  object-fit: cover;
  box-shadow: 0 0 0 1px rgb(0 0 0 / 0.08);
  vertical-align: middle;
  flex: none;
}
.ga-flag--badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  font-size: 11px;
  line-height: 1;
  font-weight: 600;
  color: var(--el-color-primary);
  background: var(--el-color-primary-light-9);
  box-shadow: 0 0 0 1px var(--el-color-primary-light-5);
}
</style>
