<script setup lang="ts">
// 面包屑（D-027）：每一级带菜单图标；分组和目录不可点击，只有页面本身。
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'

import { ArrowRight } from '@element-plus/icons-vue'

import { resolveIcon } from './icons'
import { menuTitle } from '../router/menu'

const route = useRoute()
const { t, locale } = useI18n()
const items = computed(() => {
  const crumbs = route.meta.crumbs ?? []
  const out = crumbs.map((c) => ({ key: c.name, title: menuTitle(t, locale.value, c.titleKey, c.titles), icon: resolveIcon(c.icon) }))
  if (route.meta.titleKey) {
    out.push({
      key: String(route.name ?? route.path),
      title: menuTitle(t, locale.value, route.meta.titleKey, route.meta.titles),
      icon: resolveIcon(route.meta.icon),
    })
  }
  return out
})
</script>

<template>
  <el-breadcrumb class="ga-breadcrumb" :separator-icon="ArrowRight">
    <el-breadcrumb-item v-for="c in items" :key="c.key">
      <span class="ga-breadcrumb__item">
        <el-icon v-if="c.icon"><component :is="c.icon" /></el-icon>
        <span>{{ c.title }}</span>
      </span>
    </el-breadcrumb-item>
  </el-breadcrumb>
</template>
