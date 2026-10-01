<script setup lang="ts">
// 语言选择（D-026）：旗帜 + 该语言自己的名字；只列端启用的语言。顶栏和登录页共用。
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

import GaFlag from './GaFlag.vue'
import { useLanguages } from '../context'
import { isLocaleCode, persistLocale } from '../i18n'
import type { LocaleCode } from '../i18n'

withDefaults(defineProps<{ showLabel?: boolean }>(), { showLabel: false })
const { t, locale } = useI18n()
const languages = useLanguages()
const current = computed<LocaleCode>(() => (isLocaleCode(locale.value) ? locale.value : 'en-US'))
const currentLabel = computed(() => languages.find((l) => l.code === current.value)?.label ?? '')

function setLocale(code: LocaleCode) {
  locale.value = code
  persistLocale(code)
}
</script>

<template>
  <el-dropdown trigger="click" max-height="420px" @command="setLocale">
    <span class="ga-lang" :title="t('shell.layout.language')" data-test="lang-switch">
      <GaFlag :code="current" :decorative="showLabel" />
      <span v-if="showLabel" class="ga-lang__label">{{ currentLabel }}</span>
    </span>
    <template #dropdown>
      <el-dropdown-menu>
        <el-dropdown-item v-for="l in languages" :key="l.code" :command="l.code" :disabled="l.code === current" :data-test="`lang-${l.code}`">
          <span class="ga-lang__item">
            <GaFlag :code="l.code" decorative />
            <span :lang="l.code">{{ l.label }}</span>
          </span>
        </el-dropdown-item>
      </el-dropdown-menu>
    </template>
  </el-dropdown>
</template>

<style scoped>
.ga-lang {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  cursor: pointer;
  padding: 4px;
  border-radius: 4px;
}
.ga-lang:hover {
  background: var(--el-fill-color-light);
}
.ga-lang__label {
  font-size: 13px;
}
.ga-lang__item {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  min-width: 140px;
}
</style>
