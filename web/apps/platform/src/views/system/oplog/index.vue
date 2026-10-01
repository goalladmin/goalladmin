<script setup lang="ts">
import { reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { formatTime, hasPerm, useI18n, useTable } from '@ga/shell'

import { logApi } from '../../../api/system'
import type { OperationLog } from '../../../api/system'

const { t, te } = useI18n()
const router = useRouter()
// 有调查时间线权限时，IP 和会话可以点过去看这条线（D-032）
const canTimeline = hasPerm('system:audit:timeline')

interface Q extends Record<string, unknown> {
  username: string
  action: string
  path: string
  failed: number | undefined
  range: [string, string] | null
}

const table = useTable<Q, OperationLog>({
  query: { username: '', action: '', path: '', failed: undefined, range: null },
  fetch: ({ range, ...rest }) =>
    logApi.operations({ ...rest, from: range?.[0] ? new Date(range[0]).toISOString() : undefined, to: range?.[1] ? new Date(range[1]).toISOString() : undefined }),
})

/** 动作名是稳定编码（如 user.create），有翻译就显示翻译，没有就显示编码。 */
function actionLabel(action: string) {
  const key = `op.${action}`
  return te(key) ? t(key) : action
}

const detail = reactive({ visible: false })
function toTimeline(key: 'ip' | 'sessionId', value: string) {
  if (!value) return
  detail.visible = false
  void router.push({ path: '/ops/timeline', query: { [key]: value } })
}
const current = ref<OperationLog | null>(null)
function openDetail(row: OperationLog) {
  current.value = row
  detail.visible = true
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
        <el-form-item :label="t('sys.oplog.action')">
          <el-input v-model="table.query.action" clearable placeholder="user.create" style="width: 160px" @keyup.enter="table.search()" />
        </el-form-item>
        <el-form-item :label="t('sys.oplog.path')">
          <el-input v-model="table.query.path" clearable placeholder="/api/platform/v1/system" style="width: 220px" @keyup.enter="table.search()" />
        </el-form-item>
        <el-form-item :label="t('sys.oplog.result')">
          <el-select v-model="table.query.failed" clearable :placeholder="t('common.all')" style="width: 110px">
            <el-option :value="0" :label="t('sys.oplog.ok')" />
            <el-option :value="1" :label="t('sys.oplog.failed')" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('sys.oplog.time')">
          <el-date-picker v-model="table.query.range" type="datetimerange" :start-placeholder="t('common.from')" :end-placeholder="t('common.to')" style="width: 340px" />
        </el-form-item>
        <el-form-item>
          <el-button type="primary" native-type="submit">{{ t('common.search') }}</el-button>
          <el-button @click="table.reset()">{{ t('common.reset') }}</el-button>
        </el-form-item>
      </el-form>
    </el-card>
    <el-card>
      <el-table v-loading="table.loading" :data="table.list" stripe data-test="oplog-table">
        <el-table-column :label="t('sys.oplog.time')" min-width="170">
          <template #default="{ row }">{{ formatTime((row as OperationLog).createdAt) }}</template>
        </el-table-column>
        <el-table-column prop="username" :label="t('common.username')" min-width="110" />
        <el-table-column :label="t('sys.oplog.action')" min-width="130">
          <template #default="{ row }">{{ actionLabel((row as OperationLog).action) }}</template>
        </el-table-column>
        <el-table-column :label="t('sys.oplog.request')" min-width="280" show-overflow-tooltip>
          <template #default="{ row }"><span class="ga-mono">{{ (row as OperationLog).method }} {{ (row as OperationLog).path }}</span></template>
        </el-table-column>
        <el-table-column :label="t('sys.oplog.result')" width="110">
          <template #default="{ row }">
            <el-tag :type="(row as OperationLog).code === 0 ? 'success' : 'danger'" size="small">{{ (row as OperationLog).code === 0 ? t('sys.oplog.ok') : (row as OperationLog).code }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="latencyMs" :label="t('sys.oplog.latency')" width="90" />
        <el-table-column label="IP" min-width="120">
          <template #default="{ row }">
            <el-link v-if="canTimeline && (row as OperationLog).ip" :underline="false" class="ga-mono" @click="toTimeline('ip', (row as OperationLog).ip)">{{ (row as OperationLog).ip }}</el-link>
            <span v-else class="ga-mono">{{ (row as OperationLog).ip }}</span>
          </template>
        </el-table-column>
        <el-table-column :label="t('common.actions')" width="80" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="openDetail(row as OperationLog)">{{ t('common.detail') }}</el-button>
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

    <el-dialog v-model="detail.visible" :title="t('sys.oplog.detail')" width="720px">
      <el-descriptions v-if="current" :column="2" border size="small">
        <el-descriptions-item :label="t('sys.oplog.time')">{{ formatTime(current.createdAt) }}</el-descriptions-item>
        <el-descriptions-item :label="t('common.username')">{{ current.username }} (#{{ current.userId }})</el-descriptions-item>
        <el-descriptions-item :label="t('sys.oplog.action')">{{ actionLabel(current.action) }}</el-descriptions-item>
        <el-descriptions-item :label="t('sys.oplog.result')">HTTP {{ current.httpStatus }} / code {{ current.code }}</el-descriptions-item>
        <el-descriptions-item :label="t('sys.oplog.request')" :span="2"><span class="ga-mono">{{ current.method }} {{ current.path }}</span></el-descriptions-item>
        <el-descriptions-item :label="t('sys.oplog.query')" :span="2"><span class="ga-mono">{{ current.query }}</span></el-descriptions-item>
        <el-descriptions-item :label="t('sys.oplog.body')" :span="2"><pre class="ga-mono">{{ pretty(current.body) }}</pre></el-descriptions-item>
        <el-descriptions-item :label="t('sys.oplog.error')" :span="2">{{ current.error }}</el-descriptions-item>
        <el-descriptions-item label="IP">{{ current.ip }}</el-descriptions-item>
        <el-descriptions-item :label="t('ops.secevent.session')" :span="2">
          <el-link v-if="canTimeline && current.sessionId" :underline="false" class="ga-mono" @click="toTimeline('sessionId', current.sessionId)">{{ current.sessionId }}</el-link>
          <span v-else class="ga-mono">{{ current.sessionId }}</span>
        </el-descriptions-item>
        <el-descriptions-item :label="t('sys.oplog.latency')">{{ current.latencyMs }} ms</el-descriptions-item>
        <el-descriptions-item label="User-Agent" :span="2">{{ current.userAgent }}</el-descriptions-item>
        <el-descriptions-item label="Request-Id" :span="2"><span class="ga-mono">{{ current.requestId }}</span></el-descriptions-item>
      </el-descriptions>
    </el-dialog>
  </div>
</template>
