<script setup lang="ts">
// 偏好设置抽屉（D-027）：只影响本浏览器的显示，存在 localStorage。
import { useI18n } from 'vue-i18n'

import { Check } from '@element-plus/icons-vue'

import { PrimaryPresets, useLayoutStore } from './store'

const open = defineModel<boolean>({ required: true })
const { t } = useI18n()
const layout = useLayoutStore()

// 取色器清空时给 null、有时给大写：只接受 #RRGGBB
function setPrimary(v: string | null) {
  if (v && /^#[0-9a-fA-F]{6}$/.test(v)) layout.prefs.primary = v.toLowerCase()
}
</script>

<template>
  <el-drawer v-model="open" :title="t('shell.prefs.title')" size="320px" append-to-body class="ga-prefs" data-test="prefs-drawer">
    <section class="ga-prefs__section">
      <h4>{{ t('shell.prefs.primary') }}</h4>
      <div class="ga-prefs__colors">
        <button
          v-for="c in PrimaryPresets"
          :key="c"
          type="button"
          class="ga-prefs__color"
          :style="{ background: c }"
          :aria-label="c"
          :data-test="`prefs-color-${c.slice(1)}`"
          @click="layout.prefs.primary = c"
        >
          <el-icon v-if="layout.prefs.primary === c"><Check /></el-icon>
        </button>
        <el-color-picker :model-value="layout.prefs.primary" size="small" :predefine="[...PrimaryPresets]" @change="setPrimary" />
      </div>
    </section>

    <section class="ga-prefs__section">
      <h4>{{ t('shell.prefs.appearance') }}</h4>
      <div class="ga-prefs__row">
        <span>{{ t('shell.layout.dark') }}</span>
        <el-switch :model-value="layout.dark" @update:model-value="(v: string | number | boolean) => layout.setDark(!!v)" />
      </div>
      <div class="ga-prefs__row">
        <span>{{ t('shell.prefs.sidebar') }}</span>
        <el-radio-group v-model="layout.prefs.sidebarTheme" size="small">
          <el-radio-button value="light">{{ t('shell.prefs.light') }}</el-radio-button>
          <el-radio-button value="dark">{{ t('shell.prefs.darkSidebar') }}</el-radio-button>
        </el-radio-group>
      </div>
      <div class="ga-prefs__row">
        <span>{{ t('shell.prefs.compact') }}</span>
        <el-switch v-model="layout.prefs.compact" data-test="prefs-compact" />
      </div>
    </section>

    <section class="ga-prefs__section">
      <h4>{{ t('shell.prefs.layout') }}</h4>
      <div class="ga-prefs__row">
        <span>{{ t('shell.prefs.showTabs') }}</span>
        <el-switch v-model="layout.prefs.showTabs" data-test="prefs-tabs" />
      </div>
      <div class="ga-prefs__row">
        <span>{{ t('shell.prefs.showBreadcrumb') }}</span>
        <el-switch v-model="layout.prefs.showBreadcrumb" />
      </div>
    </section>

    <template #footer>
      <el-button class="ga-prefs__reset" @click="layout.resetPreferences()">{{ t('shell.prefs.reset') }}</el-button>
      <div class="ga-prefs__hint">{{ t('shell.prefs.hint') }}</div>
    </template>
  </el-drawer>
</template>
