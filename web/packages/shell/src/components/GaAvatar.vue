<script setup lang="ts">
// 头像（D-040）：内置头像直接画，上传的按键向服务器要（登录后才读得到）；没有头像时显示账号的首字母，
// 底色和文字色按账号从 100 组配色里固定挑一组。
import { computed, ref, watch } from 'vue'

import { avatarColor, avatarInitial, loadUploadedAvatar, parseAvatar, presetAvatarSrc } from '../avatar'
import { avatarPath, usePortal } from '../context'

const props = withDefaults(
  defineProps<{
    /** 用户的 avatar 值：空、preset:<名字> 或 upload:<键>。 */
    value?: string | null
    /** 账号：取首字母，也按它挑底色。 */
    name?: string | null
    size?: number
  }>(),
  { value: '', name: '', size: 32 },
)

const uploaded = ref<string | null>(null)
const parsed = computed(() => parseAvatar(props.value))

watch(
  () => [props.value, props.size] as const,
  async () => {
    uploaded.value = null
    const r = parsed.value
    if (r.kind !== 'upload') return
    const want = props.value
    const size = props.size
    // 64×64 的小图只够 32px 以内在高分屏上显示清楚，再大就取 256×256 的
    const img = await loadUploadedAvatar(usePortal().client, avatarPath(), r.key, size <= 32)
    if (props.value === want && props.size === size) uploaded.value = img
  },
  { immediate: true },
)

const colors = computed(() => avatarColor(props.name))

const src = computed(() => {
  const r = parsed.value
  if (r.kind === 'preset') return presetAvatarSrc(r.name)
  if (r.kind === 'upload') return uploaded.value ?? undefined
  return undefined
})
</script>

<template>
  <el-avatar :size="size" :src="src" class="ga-avatar" :style="{ backgroundColor: colors.bg, color: colors.fg, fontSize: `${Math.round(size * 0.42)}px`, fontWeight: 600 }">{{ avatarInitial(name) }}</el-avatar>
</template>
