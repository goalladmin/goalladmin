<script setup lang="ts">
// 加一条 IP 黑名单（D-062）：黑名单页的"添加"，以及安全事件、登录日志里的"封禁这个 IP"。
// 黑名单对平台、代理商、商户三个程序都生效；不能盖住自己当前的 IP（服务端判断）。
import { reactive, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { describeApiError, isApiError, useI18n } from '@ga/shell'

import { ipApi } from '../api/system'

const props = defineProps<{ modelValue: boolean; ip?: string }>()
const emit = defineEmits<{ 'update:modelValue': [boolean]; done: [] }>()
const { t, te } = useI18n()

/** 有效期选项（分钟），0 为永久。 */
const durations = [60, 24 * 60, 7 * 24 * 60, 30 * 24 * 60, 0]
const form = reactive({ cidr: '', expiresIn: 24 * 60, remark: '' })
const saving = ref(false)
const error = ref('')

watch(
  () => props.modelValue,
  (open) => {
    if (open) {
      Object.assign(form, { cidr: props.ip ?? '', expiresIn: 24 * 60, remark: '' })
      error.value = ''
    }
  },
)

function durationLabel(m: number) {
  if (m === 0) return t('ipacl.forever')
  if (m % (24 * 60) === 0) return t('ipacl.days', { n: m / (24 * 60) })
  return t('ipacl.hours', { n: m / 60 })
}

async function submit() {
  error.value = ''
  if (!form.cidr.trim()) {
    error.value = t('common.required')
    return
  }
  saving.value = true
  try {
    await ipApi.addDeny({ cidr: form.cidr.trim(), expiresIn: form.expiresIn, remark: form.remark.trim() })
    ElMessage.success(t('common.success'))
    emit('update:modelValue', false)
    emit('done')
  } catch (e) {
    if (isApiError(e)) error.value = describeApiError(e, (k, p) => t(k, p ?? {}), (k) => te(k) || te(k, 'en-US')).join(t('shell.error.listSep'))
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <el-dialog :model-value="modelValue" :title="t('ipacl.blockTitle')" width="480px" @update:model-value="(v: boolean) => emit('update:modelValue', v)">
    <el-alert :title="t('ipacl.blockHint')" type="warning" :closable="false" show-icon />
    <p class="ga-form-hint">{{ t('ipacl.wildcardHint') }}</p>
    <el-form label-width="96px" class="ga-ipblock__form" @submit.prevent="submit">
      <el-form-item :label="t('ipacl.cidr')" required>
        <el-input v-model="form.cidr" :placeholder="t('ipacl.cidrPlaceholder')" maxlength="64" data-test="ip-block-cidr" />
      </el-form-item>
      <el-form-item :label="t('ipacl.duration')">
        <el-select v-model="form.expiresIn" style="width: 160px" data-test="ip-block-duration">
          <el-option v-for="m in durations" :key="m" :value="m" :label="durationLabel(m)" />
        </el-select>
      </el-form-item>
      <el-form-item :label="t('common.remark')">
        <el-input v-model="form.remark" maxlength="255" />
      </el-form-item>
    </el-form>
    <el-alert v-if="error" :title="error" type="error" :closable="false" show-icon data-test="ip-block-error" />
    <template #footer>
      <el-button @click="emit('update:modelValue', false)">{{ t('common.cancel') }}</el-button>
      <el-button type="danger" :loading="saving" data-test="ip-block-submit" @click="submit">{{ t('ipacl.block') }}</el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.ga-ipblock__form {
  margin-top: 16px;
}
</style>
