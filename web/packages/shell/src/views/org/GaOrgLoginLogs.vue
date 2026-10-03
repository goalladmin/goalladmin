<script setup lang="ts">
// 主体端的登录日志（D-067）：只有本主体的（包括有人拿本主体的编号、输错账号名的失败登录），只读；每次查看都记操作日志。
import { useI18n } from 'vue-i18n'

import { useTable } from '../../composables/useTable'
import { formatTime } from '../../format'
import { orgApi } from '../../org/api'
import type { OrgLoginLog } from '../../org/api'

const { t, te } = useI18n()

interface Q extends Record<string, unknown> {
  username: string
  ip: string
  success: number | undefined
  range: [string, string] | null
}

const table = useTable<Q, OrgLoginLog>({
  query: { username: '', ip: '', success: undefined, range: null },
  fetch: ({ range, ...rest }) =>
    orgApi.loginLogs({ ...rest, from: range?.[0] ? new Date(range[0]).toISOString() : undefined, to: range?.[1] ? new Date(range[1]).toISOString() : undefined }),
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
        <el-form-item :label="t('org.log.result')">
          <el-select v-model="table.query.success" clearable :placeholder="t('common.all')" style="width: 110px">
            <el-option :value="1" :label="t('org.log.success')" />
            <el-option :value="0" :label="t('org.log.failed')" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('org.log.time')">
          <el-date-picker v-model="table.query.range" type="datetimerange" :start-placeholder="t('common.from')" :end-placeholder="t('common.to')" style="width: 340px" />
        </el-form-item>
        <el-form-item>
          <el-button type="primary" native-type="submit">{{ t('common.search') }}</el-button>
          <el-button @click="table.reset()">{{ t('common.reset') }}</el-button>
        </el-form-item>
      </el-form>
    </el-card>
    <el-card>
      <el-table v-loading="table.loading" :data="table.list" stripe data-test="org-loginlog-table">
        <el-table-column :label="t('org.log.time')" min-width="170">
          <template #default="{ row }">{{ formatTime((row as OrgLoginLog).createdAt) }}</template>
        </el-table-column>
        <el-table-column prop="username" :label="t('common.username')" min-width="120" />
        <el-table-column :label="t('org.log.result')" width="90">
          <template #default="{ row }">
            <el-tag :type="(row as OrgLoginLog).success ? 'success' : 'danger'" size="small">{{ (row as OrgLoginLog).success ? t('org.log.success') : t('org.log.failed') }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column :label="t('org.log.reason')" min-width="140">
          <template #default="{ row }">{{ reasonLabel((row as OrgLoginLog).reason) }}</template>
        </el-table-column>
        <el-table-column prop="ip" label="IP" min-width="140" />
        <el-table-column :label="t('org.log.session')" min-width="100">
          <template #default="{ row }"><span class="ga-mono" :title="(row as OrgLoginLog).sessionId">{{ (row as OrgLoginLog).sessionId.slice(0, 8) }}</span></template>
        </el-table-column>
        <el-table-column prop="userAgent" label="User-Agent" min-width="240" show-overflow-tooltip />
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
