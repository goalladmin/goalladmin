<script setup lang="ts">
// 主体端的操作日志（D-067）：只有本主体账号的操作，只读；每次查看都记操作日志。请求参数里的密码、密钥类字段已经打码（规范 §10）。
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { useTable } from '../../composables/useTable'
import { formatTime } from '../../format'
import { orgApi } from '../../org/api'
import type { OrgOperationLog } from '../../org/api'

const { t, te } = useI18n()

interface Q extends Record<string, unknown> {
  username: string
  action: string
  failed: number | undefined
  range: [string, string] | null
}

const table = useTable<Q, OrgOperationLog>({
  query: { username: '', action: '', failed: undefined, range: null },
  fetch: ({ range, ...rest }) =>
    orgApi.operationLogs({ ...rest, from: range?.[0] ? new Date(range[0]).toISOString() : undefined, to: range?.[1] ? new Date(range[1]).toISOString() : undefined }),
})

/** 动作名是稳定编码（如 org.account.create），有翻译就显示翻译，没有就显示编码。 */
function actionLabel(action: string) {
  const key = `op.${action}`
  return te(key) ? t(key) : action
}

const current = ref<OrgOperationLog | null>(null)
const detail = ref(false)
function openDetail(row: OrgOperationLog) {
  current.value = row
  detail.value = true
}

function pretty(s: string) {
  if (!s) return ''
  try {
    return JSON.stringify(JSON.parse(s), null, 2)
  } catch {
    return s
  }
}
</script>

<template>
  <div class="ga-page">
    <el-card>
      <el-form class="ga-toolbar" :inline="true" @submit.prevent="table.search()">
        <el-form-item :label="t('common.username')">
          <el-input v-model="table.query.username" clearable style="width: 140px" @keyup.enter="table.search()" />
        </el-form-item>
        <el-form-item :label="t('org.log.action')">
          <el-input v-model="table.query.action" clearable placeholder="org.account.create" style="width: 180px" @keyup.enter="table.search()" />
        </el-form-item>
        <el-form-item :label="t('org.log.result')">
          <el-select v-model="table.query.failed" clearable :placeholder="t('common.all')" style="width: 110px">
            <el-option :value="0" :label="t('org.log.success')" />
            <el-option :value="1" :label="t('org.log.failed')" />
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
      <el-table v-loading="table.loading" :data="table.list" stripe data-test="org-oplog-table">
        <el-table-column :label="t('org.log.time')" min-width="170">
          <template #default="{ row }">{{ formatTime((row as OrgOperationLog).createdAt) }}</template>
        </el-table-column>
        <el-table-column prop="username" :label="t('common.username')" min-width="110" />
        <el-table-column :label="t('org.log.action')" min-width="140">
          <template #default="{ row }">{{ actionLabel((row as OrgOperationLog).action) }}</template>
        </el-table-column>
        <el-table-column :label="t('org.log.request')" min-width="260" show-overflow-tooltip>
          <template #default="{ row }"><span class="ga-mono">{{ (row as OrgOperationLog).method }} {{ (row as OrgOperationLog).path }}</span></template>
        </el-table-column>
        <el-table-column :label="t('org.log.result')" width="100">
          <template #default="{ row }">
            <el-tag :type="(row as OrgOperationLog).code === 0 ? 'success' : 'danger'" size="small">{{ (row as OrgOperationLog).code === 0 ? t('org.log.success') : (row as OrgOperationLog).code }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="latencyMs" :label="t('org.log.latency')" width="90" />
        <el-table-column prop="ip" label="IP" min-width="120" />
        <el-table-column :label="t('common.actions')" width="80" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="openDetail(row as OrgOperationLog)">{{ t('common.detail') }}</el-button>
          </template>
        </el-table-column>
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

    <el-dialog v-model="detail" :title="t('common.detail')" width="720px">
      <el-descriptions v-if="current" :column="2" border size="small">
        <el-descriptions-item :label="t('org.log.time')">{{ formatTime(current.createdAt) }}</el-descriptions-item>
        <el-descriptions-item :label="t('common.username')">{{ current.username }} (#{{ current.userId }})</el-descriptions-item>
        <el-descriptions-item :label="t('org.log.action')">{{ actionLabel(current.action) }}</el-descriptions-item>
        <el-descriptions-item :label="t('org.log.result')">HTTP {{ current.httpStatus }} / code {{ current.code }}</el-descriptions-item>
        <el-descriptions-item :label="t('org.log.request')" :span="2"><span class="ga-mono">{{ current.method }} {{ current.path }}</span></el-descriptions-item>
        <el-descriptions-item :label="t('org.log.query')" :span="2"><span class="ga-mono">{{ current.query }}</span></el-descriptions-item>
        <el-descriptions-item :label="t('org.log.body')" :span="2"><pre class="ga-mono">{{ pretty(current.body) }}</pre></el-descriptions-item>
        <el-descriptions-item :label="t('org.log.error')" :span="2">{{ current.error }}</el-descriptions-item>
        <el-descriptions-item label="IP">{{ current.ip }}</el-descriptions-item>
        <el-descriptions-item :label="t('org.log.latency')">{{ current.latencyMs }} ms</el-descriptions-item>
        <el-descriptions-item :label="t('org.log.session')" :span="2"><span class="ga-mono">{{ current.sessionId }}</span></el-descriptions-item>
        <el-descriptions-item label="User-Agent" :span="2">{{ current.userAgent }}</el-descriptions-item>
        <el-descriptions-item label="Request-Id" :span="2"><span class="ga-mono">{{ current.requestId }}</span></el-descriptions-item>
      </el-descriptions>
    </el-dialog>
  </div>
</template>
