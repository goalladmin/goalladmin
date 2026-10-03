<script setup lang="ts">
// 运维中心的"端"筛选（D-066）：平台端（默认）、代理商端、商户端。三个程序共用一个库，平台的运维中心都看得到。
import { useI18n } from '@ga/shell'

import { opsPortals } from '../api/partner'
import type { OpsPortal } from '../api/partner'

defineProps<{ modelValue: OpsPortal }>()
const emit = defineEmits<{ 'update:modelValue': [OpsPortal] }>()
const { t } = useI18n()
</script>

<template>
  <el-select :model-value="modelValue" style="width: 120px" data-test="ops-portal" @update:model-value="emit('update:modelValue', $event)">
    <el-option v-for="p in opsPortals" :key="p" :value="p" :label="t(`ops.portal.${p}`)" />
  </el-select>
</template>
