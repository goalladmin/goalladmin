<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

import { resolveIcon } from './icons'
import { menuIndex, menuTitle } from '../router/menu'
import type { MenuTree } from '../types'

const props = defineProps<{ node: MenuTree }>()
const { t, locale } = useI18n()
const title = computed(() => menuTitle(t, locale.value, props.node.titleKey, props.node.titles))
const icon = computed(() => resolveIcon(props.node.icon))
const children = computed(() => (props.node.children ?? []).filter((c) => !c.hidden).sort((a, b) => a.sort - b.sort))
</script>

<template>
  <el-sub-menu v-if="children.length" :index="menuIndex(node)">
    <template #title>
      <el-icon v-if="icon"><component :is="icon" /></el-icon>
      <span>{{ title }}</span>
    </template>
    <GaMenuItem v-for="c in children" :key="c.name" :node="c" />
  </el-sub-menu>
  <el-menu-item v-else-if="node.component" :index="node.path">
    <el-icon v-if="icon"><component :is="icon" /></el-icon>
    <template #title>{{ title }}</template>
  </el-menu-item>
</template>
