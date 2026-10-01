<script setup lang="ts">
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()
const router = useRouter()
</script>

<template>
  <div class="ga-page">
    <el-result icon="error" :title="t('shell.error.notFound')" :sub-title="t('shell.error.notFoundDesc')">
      <template #extra>
        <el-button type="primary" @click="router.replace('/')">{{ t('shell.error.back') }}</el-button>
      </template>
    </el-result>
  </div>
</template>
