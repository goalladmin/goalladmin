<script setup lang="ts">
// 多语言文字的输入（D-026）：端启用的每种语言一行，前面是旗帜和语言名。
// v-model 是 { 语言代码: 文字 }；留空的语言不写进结果（显示时按回退链）。exclude 用来去掉写在主字段里的默认语言。
import { computed } from 'vue'

import GaFlag from './GaFlag.vue'
import { useLanguages } from '../context'
import type { LocaleCode } from '../i18n/languages'

const props = withDefaults(
  defineProps<{
    modelValue: Record<string, string>
    exclude?: LocaleCode[]
    maxlength?: number
    /** 每种语言的占位提示（例如代码里的默认文字）。 */
    placeholder?: (code: LocaleCode) => string
    /** 必填的语言：输入框标红星（校验由表单负责）。 */
    required?: LocaleCode[]
    testId?: string
  }>(),
  { exclude: () => [], maxlength: 64, placeholder: undefined, required: () => [], testId: 'i18n' },
)
const emit = defineEmits<{ 'update:modelValue': [Record<string, string>] }>()

const languages = computed(() => useLanguages().filter((l) => !props.exclude.includes(l.code)))

function set(code: LocaleCode, v: string) {
  const next = { ...props.modelValue }
  if (v.trim()) next[code] = v
  else delete next[code]
  emit('update:modelValue', next)
}
</script>

<template>
  <div class="ga-i18n-inputs">
    <div v-for="l in languages" :key="l.code" class="ga-i18n-inputs__row">
      <span class="ga-i18n-inputs__lang" :title="l.label">
        <GaFlag :code="l.code" decorative />
        <span :lang="l.code">{{ l.label }}</span>
        <span v-if="required.includes(l.code)" class="ga-i18n-inputs__req">*</span>
      </span>
      <el-input
        :model-value="modelValue[l.code] ?? ''"
        :maxlength="maxlength"
        :placeholder="placeholder?.(l.code) ?? ''"
        :lang="l.code"
        :data-test="`${testId}-${l.code}`"
        @update:model-value="(v: string) => set(l.code, v)"
      />
    </div>
  </div>
</template>

<style scoped>
.ga-i18n-inputs {
  display: flex;
  flex-direction: column;
  gap: 6px;
  width: 100%;
}
.ga-i18n-inputs__row {
  display: grid;
  grid-template-columns: 132px 1fr;
  align-items: center;
  gap: 8px;
}
.ga-i18n-inputs__lang {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--el-text-color-regular);
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}
.ga-i18n-inputs__req {
  color: var(--el-color-danger);
}
</style>
