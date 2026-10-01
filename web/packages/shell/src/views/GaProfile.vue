<script setup lang="ts">
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'

import { useAuthStore } from '../auth/store'
import { RouteNames } from '../router'

const { t } = useI18n()
const router = useRouter()
const auth = useAuthStore()
</script>

<template>
  <div class="ga-page">
    <el-card>
      <el-descriptions :title="t('shell.profile.title')" :column="1" border>
        <el-descriptions-item :label="t('shell.profile.username')">{{ auth.user?.username }}</el-descriptions-item>
        <el-descriptions-item :label="t('shell.profile.displayName')">{{ auth.user?.displayName }}</el-descriptions-item>
        <el-descriptions-item :label="t('shell.profile.super')">
          <el-tag v-if="auth.user?.super" type="danger">{{ t('common.yes') }}</el-tag>
          <span v-else>{{ t('common.no') }}</span>
        </el-descriptions-item>
        <el-descriptions-item :label="t('shell.profile.perms')">
          <span v-if="auth.user?.super">*</span>
          <el-tag v-for="p in auth.perms" v-else :key="p" size="small" class="ga-tag">{{ p }}</el-tag>
        </el-descriptions-item>
      </el-descriptions>
      <div class="ga-page__actions">
        <el-button type="primary" @click="router.push({ name: RouteNames.changePassword })">{{ t('shell.profile.changePassword') }}</el-button>
      </div>
    </el-card>
  </div>
</template>
