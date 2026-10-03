<script setup lang="ts">
// 主账号的初始密码（开户、重置之后）：只显示这一次（D-065），关掉就再也看不到了。
// 密码由父组件拿着，父组件在对话框关闭时清空它（D-098）；这里关闭时销毁内容。
import { ElMessage } from 'element-plus'
import { useI18n } from '@ga/shell'

defineProps<{ modelValue: boolean; title: string; code: string; username: string; password: string }>()
const emit = defineEmits<{ 'update:modelValue': [boolean] }>()
const { t } = useI18n()

async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    ElMessage.success(t('common.copied'))
  } catch {
    // 非安全上下文没有剪贴板，用户手动复制
  }
}
</script>

<template>
  <el-dialog :model-value="modelValue" :title="title" width="460px" destroy-on-close data-test="org-password" @update:model-value="emit('update:modelValue', $event)">
    <el-alert :title="t('partner.passwordHint')" type="warning" :closable="false" show-icon />
    <el-descriptions :column="1" border style="margin-top: 12px">
      <el-descriptions-item :label="t('partner.code')">
        <span class="ga-mono" data-test="org-password-code">{{ code }}</span>
      </el-descriptions-item>
      <el-descriptions-item :label="t('common.username')">{{ username }}</el-descriptions-item>
      <el-descriptions-item :label="t('partner.password')">
        <span class="ga-mono" data-test="org-password-value">{{ password }}</span>
        <el-button link type="primary" style="margin-left: 8px" @click="copy(password)">{{ t('common.copy') }}</el-button>
      </el-descriptions-item>
    </el-descriptions>
    <template #footer>
      <el-button type="primary" @click="emit('update:modelValue', false)">{{ t('common.close') }}</el-button>
    </template>
  </el-dialog>
</template>
