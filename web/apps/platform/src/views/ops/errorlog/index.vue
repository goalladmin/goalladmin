<script setup lang="ts">
// 错误日志（D-032）：服务端故障按类合并，只读。调用栈要单独的权限。可以选看代理商端、商户端的（D-066）。
import { ref } from 'vue'
import { ElMessage } from 'element-plus'
import { formatTime, hasPerm, useI18n, useTable } from '@ga/shell'

import PortalSelect from '../../../components/PortalSelect.vue'
import { logApi } from '../../../api/system'
import type { ErrorLog } from '../../../api/system'
import type { OpsPortal } from '../../../api/partner'

const { t } = useI18n()

interface Q extends Record<string, unknown> {
  portal: OpsPortal
  kind: string
  route: string
  range: [string, string] | null
}

const table = useTable<Q, ErrorLog>({
  query: { portal: 'platform', kind: '', route: '', range: null },
  fetch: ({ range, ...rest }) =>
    logApi.errors({ ...rest, from: range?.[0] ? new Date(range[0]).toISOString() : undefined, to: range?.[1] ? new Date(range[1]).toISOString() : undefined }),
})

const detailVisible = ref(false)
const current = ref<ErrorLog | null>(null)
const canStack = hasPerm('system:errorlog:detail')
// 打开序号：连续打开两条时，前一条的慢响应不能盖住当前这条的详情（D-046）
let detailSeq = 0
async function openDetail(row: ErrorLog) {
  const my = ++detailSeq
  current.value = row
  detailVisible.value = true
  if (!canStack) return
  try {
    const d = await logApi.error(row.id)
    if (my === detailSeq) current.value = d
  } catch {
    // 请求层已提示
  }
}

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
  <div class="ga-page">
    <el-alert :title="t('ops.errorlog.hint')" type="info" :closable="false" show-icon />
    <el-card>
      <el-form class="ga-toolbar" :inline="true" @submit.prevent="table.search()">
        <el-form-item :label="t('ops.portal.label')">
          <PortalSelect v-model="table.query.portal" @update:model-value="table.search()" />
        </el-form-item>
        <el-form-item :label="t('ops.errorlog.kind')">
          <el-select v-model="table.query.kind" clearable :placeholder="t('common.all')" style="width: 130px">
            <el-option value="panic" :label="t('ops.errorlog.kindPanic')" />
            <el-option value="error" :label="t('ops.errorlog.kindError')" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('ops.errorlog.route')">
          <el-input v-model="table.query.route" clearable :placeholder="`/api/${table.query.portal}/v1`" style="width: 240px" @keyup.enter="table.search()" />
        </el-form-item>
        <el-form-item :label="t('ops.errorlog.lastAt')">
          <el-date-picker v-model="table.query.range" type="datetimerange" :start-placeholder="t('common.from')" :end-placeholder="t('common.to')" style="width: 340px" />
        </el-form-item>
        <el-form-item>
          <el-button type="primary" native-type="submit">{{ t('common.search') }}</el-button>
          <el-button @click="table.reset()">{{ t('common.reset') }}</el-button>
        </el-form-item>
      </el-form>
    </el-card>
    <el-card>
      <el-table v-loading="table.loading" :data="table.list" stripe data-test="errorlog-table">
        <el-table-column :label="t('ops.errorlog.lastAt')" min-width="170">
          <template #default="{ row }">{{ formatTime((row as ErrorLog).lastAt) }}</template>
        </el-table-column>
        <el-table-column :label="t('ops.errorlog.kind')" width="120">
          <template #default="{ row }">
            <el-tag :type="(row as ErrorLog).kind === 'panic' ? 'danger' : 'warning'" size="small">
              {{ (row as ErrorLog).kind === 'panic' ? t('ops.errorlog.kindPanic') : t('ops.errorlog.kindError') }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column :label="t('ops.errorlog.route')" min-width="260" show-overflow-tooltip>
          <template #default="{ row }">
            <span class="ga-mono">{{ (row as ErrorLog).method }} {{ (row as ErrorLog).route || t('ops.errorlog.unmatched') }}</span>
          </template>
        </el-table-column>
        <el-table-column :label="t('ops.errorlog.message')" min-width="280" show-overflow-tooltip>
          <template #default="{ row }"><span class="ga-mono">{{ (row as ErrorLog).message }}</span></template>
        </el-table-column>
        <el-table-column :label="t('ops.errorlog.count')" width="90" align="right">
          <template #default="{ row }"><b>{{ (row as ErrorLog).count }}</b></template>
        </el-table-column>
        <el-table-column :label="t('ops.errorlog.firstAt')" min-width="170">
          <template #default="{ row }">{{ formatTime((row as ErrorLog).firstAt) }}</template>
        </el-table-column>
        <el-table-column :label="t('common.actions')" width="80" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="openDetail(row as ErrorLog)">{{ t('common.detail') }}</el-button>
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

    <el-dialog v-model="detailVisible" :title="t('ops.errorlog.detail')" width="820px">
      <el-descriptions v-if="current" :column="2" border size="small">
        <el-descriptions-item :label="t('ops.errorlog.kind')">
          {{ current.kind === 'panic' ? t('ops.errorlog.kindPanic') : t('ops.errorlog.kindError') }}
        </el-descriptions-item>
        <el-descriptions-item :label="t('ops.errorlog.status')">HTTP {{ current.httpStatus }} / code {{ current.code }}</el-descriptions-item>
        <el-descriptions-item :label="t('ops.errorlog.route')" :span="2">
          <span class="ga-mono">{{ current.method }} {{ current.route || t('ops.errorlog.unmatched') }}</span>
        </el-descriptions-item>
        <el-descriptions-item :label="t('ops.errorlog.message')" :span="2"><span class="ga-mono">{{ current.message }}</span></el-descriptions-item>
        <el-descriptions-item :label="t('ops.errorlog.count')">{{ current.count }}</el-descriptions-item>
        <el-descriptions-item :label="t('ops.errorlog.firstAt')">{{ formatTime(current.firstAt) }}</el-descriptions-item>
        <el-descriptions-item :label="t('ops.errorlog.lastAt')">{{ formatTime(current.lastAt) }}</el-descriptions-item>
        <el-descriptions-item :label="t('ops.errorlog.lastUser')">
          {{ current.lastUsername ? `${current.lastUsername} (#${current.lastUserId})` : '-' }}
        </el-descriptions-item>
        <el-descriptions-item label="IP">{{ current.lastIp }}</el-descriptions-item>
        <el-descriptions-item :label="t('ops.errorlog.session')"><span class="ga-mono">{{ current.lastSessionId || '-' }}</span></el-descriptions-item>
        <el-descriptions-item label="Request-Id" :span="2">
          <span class="ga-mono">{{ current.lastRequestId }}</span>
          <el-button v-if="current.lastRequestId" link type="primary" style="margin-left: 8px" @click="copy(current.lastRequestId)">{{ t('common.copy') }}</el-button>
        </el-descriptions-item>
        <el-descriptions-item :label="t('ops.errorlog.stack')" :span="2">
          <pre v-if="current.stack" class="ga-mono ga-errorlog__stack" data-test="errorlog-stack">{{ current.stack }}</pre>
          <span v-else-if="!canStack" class="ga-errorlog__muted">{{ t('ops.errorlog.noStackPerm') }}</span>
          <span v-else class="ga-errorlog__muted">{{ t('ops.errorlog.noStack') }}</span>
        </el-descriptions-item>
      </el-descriptions>
      <p class="ga-errorlog__muted ga-errorlog__tip">{{ t('ops.errorlog.requestIdHint') }}</p>
    </el-dialog>
  </div>
</template>

<style scoped>
.ga-errorlog__stack {
  max-height: 360px;
  margin: 0;
  overflow: auto;
  font-size: 12px;
  line-height: 1.5;
  white-space: pre;
}
.ga-errorlog__muted {
  color: var(--el-text-color-secondary);
}
.ga-errorlog__tip {
  margin: 12px 0 0;
  font-size: 12px;
}
</style>
