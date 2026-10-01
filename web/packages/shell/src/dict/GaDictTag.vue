<script setup lang="ts">
// 按字典把值显示成带颜色的标签：<GaDictTag code="order.priority" :value="row.priority" />
// 字典里找不到这个值时显示原始值（不加标签样式）。
import { computed } from 'vue'

import { dictTagProps, useDict } from './index'

const props = withDefaults(defineProps<{ code: string; value: unknown; size?: 'large' | 'default' | 'small' }>(), { size: 'small' })

const dict = useDict(() => props.code)
const item = computed(() => dict.item(props.value))
const tag = computed(() => dictTagProps(item.value?.color))
</script>

<template>
  <el-tag v-if="item" :type="tag.type" :style="tag.style" :size="size" disable-transitions>{{ item.label }}</el-tag>
  <span v-else>{{ value ?? '' }}</span>
</template>
