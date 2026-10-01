<script setup lang="ts">
import { useRouter } from 'vue-router'
import { formatTime, hasPerm, useI18n, useTable } from '@ga/shell'

import { logApi } from '../../../api/system'
import type { LoginLog } from '../../../api/system'

const { t, te } = useI18n()
const router = useRouter()
// 有调查时间线权限时，IP 和会话可以点过去看这条线（D-032）
const canTimeline = hasPerm('system:audit:timeline')
function toTimeline(key: 'ip' | 'sessionId', value: string) {
  if (value) void router.push({ path: '/ops/timeline', query: { [key]: value } })
}

interface Q extends Record<string, unknown> {
  username: string
  ip: string
  success: number | undefined
  range: [string, string] | null
}

const table = useTable<Q, LoginLog>({
  query: { username: '', ip: '', success: undefined, range: null },
  fetch: ({ range, ...rest }) =>
    logApi.logins({ ...rest, from: range?.[0] ? new Date(range[0]).toISOString() : undefined, to: range?.[1] ? new Date(range[1]).toISOString() : undefined }),
})

function reasonLabel(reason: string) {
  if (!reason) return ''
  const key = `loginReason.${reason}`
  return te(key) ? t(key) : reason
}
</script>

<template>
  <div class="ga-page">
    <el-card>
      <el-form class="ga-toolbar" :inline="true" @submit.prevent="table.search()">
        <el-form-item :label="t('common.username')">
          <el-input v-model="table.query.username" clearable style="width: 140px" @keyup.enter="table.search()" />
        </el-form-item>
        <el-form-item label="IP">
          <el-input v-model="table.query.ip" clearable style="width: 140px" @keyup.enter="table.search()" />
        </el-form-item>
        <el-form-item :label="t('sys.loginlog.result')">
          <el-select v-model="table.query.success" clearable :placeholder="t('common.all')" style="width: 110px">
            <el-option :value="1" :label="t('sys.loginlog.success')" />
            <el-option :value="0" :label="t('sys.loginlog.failed')" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('sys.loginlog.time')">
          <el-date-picker v-model="table.query.range" type="datetimerange" :start-placeholder="t('common.from')" :end-placeholder="t('common.to')" style="width: 340px" />
        </el-form-item>
        <el-form-item>
          <el-button type="primary" native-type="submit">{{ t('common.search') }}</el-button>
          <el-button @click="table.reset()">{{ t('common.reset') }}</el-button>
        </el-form-item>
      </el-form>
    </el-card>
    <el-card>
      <el-table v-loading="table.loading" :data="table.list" stripe data-test="loginlog-table">
        <el-table-column :label="t('sys.loginlog.time')" min-width="170">
          <template #default="{ row }">{{ formatTime((row as LoginLog).createdAt) }}</template>
        </el-table-column>
        <el-table-column prop="username" :label="t('common.username')" min-width="120" />
        <el-table-column :label="t('sys.loginlog.result')" width="90">
          <template #default="{ row }">
            <el-tag :type="(row as LoginLog).success ? 'success' : 'danger'" size="small">{{ (row as LoginLog).success ? t('sys.loginlog.success') : t('sys.loginlog.failed') }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column :label="t('sys.loginlog.reason')" min-width="140">
          <template #default="{ row }">{{ reasonLabel((row as LoginLog).reason) }}</template>
        </el-table-column>
        <el-table-column label="IP" min-width="130">
          <template #default="{ row }">
            <el-link v-if="canTimeline && (row as LoginLog).ip" :underline="false" class="ga-mono" @click="toTimeline('ip', (row as LoginLog).ip)">{{ (row as LoginLog).ip }}</el-link>
            <span v-else class="ga-mono">{{ (row as LoginLog).ip }}</span>
          </template>
        </el-table-column>
        <el-table-column :label="t('ops.secevent.session')" min-width="110">
          <template #default="{ row }">
            <el-link
              v-if="canTimeline && (row as LoginLog).sessionId"
              :underline="false"
              class="ga-mono"
              :title="(row as LoginLog).sessionId"
              @click="toTimeline('sessionId', (row as LoginLog).sessionId)"
            >
              {{ (row as LoginLog).sessionId.slice(0, 8) }}
            </el-link>
            <span v-else class="ga-mono" :title="(row as LoginLog).sessionId">{{ (row as LoginLog).sessionId.slice(0, 8) }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="userAgent" label="User-Agent" min-width="240" show-overflow-tooltip />
        <el-table-column prop="requestId" label="Request-Id" min-width="200" show-overflow-tooltip />
      </el-table>
      <div class="ga-pagination">
        <el-pagination
          v-model:current-page="table.page"
          v-model:page-size="table.pageSize"
          :total="table.total"
          :page-sizes="[20, 50, 100]"
          layout="total, sizes, prev, pager, next"
          @current-change="table.onPageChange"
          @size-change="table.onSizeChange"
        />
      </div>
    </el-card>
  </div>
</template>
