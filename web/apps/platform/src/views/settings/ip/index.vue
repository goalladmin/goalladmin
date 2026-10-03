<script setup lang="ts">
// IP 访问控制（D-062）：黑名单（对平台、代理商、商户三个程序都生效）和平台端白名单。
// 改动在本程序立即生效，另外两个程序最长 5 秒跟上。配错白名单把自己挡在外面时，在服务器上用 server ip clear-allow 清空。
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { describeApiError, formatTime, hasPerm, isApiError, useI18n, useTable } from '@ga/shell'

import IpAllowEditor from '../../../components/IpAllowEditor.vue'
import IpBlockDialog from '../../../components/IpBlockDialog.vue'
import { ipApi } from '../../../api/system'
import type { IPEntry, IPRule } from '../../../api/system'

const { t, te } = useI18n()
const tab = ref<'deny' | 'allow'>('deny')

// ---- 黑名单 ----
const canDeny = hasPerm('system:ip:deny')
const table = useTable<{ keyword: string; includeExpired: boolean }, IPRule>({
  query: { keyword: '', includeExpired: false },
  fetch: ({ includeExpired, ...rest }) => ipApi.denyList({ ...rest, includeExpired: includeExpired ? 1 : undefined }),
})
const blockOpen = ref(false)
const expired = (r: IPRule) => r.expiresAt !== null && new Date(r.expiresAt).getTime() <= Date.now()

async function unblock(r: IPRule) {
  try {
    await ElMessageBox.confirm(t('ipacl.confirmUnblock', { cidr: r.cidr }), t('common.confirmTitle'), { type: 'warning' })
  } catch {
    return
  }
  await ipApi.removeDeny(r.id)
  ElMessage.success(t('common.success'))
  await table.reload()
}

// ---- 平台端白名单 ----
const canAllow = hasPerm('system:ip:allow')
const allow = reactive({ items: [] as IPEntry[], yourIp: '', loading: false, saving: false, error: '' })
async function loadAllow() {
  allow.error = ''
  allow.loading = true
  try {
    const v = await ipApi.allow()
    allow.items = v.items.map((r) => ({ cidr: r.cidr, remark: r.remark }))
    allow.yourIp = v.yourIp
  } finally {
    allow.loading = false
  }
}
async function saveAllow() {
  allow.error = ''
  const items = allow.items.filter((e) => e.cidr.trim() !== '').map((e) => ({ cidr: e.cidr.trim(), remark: e.remark.trim() }))
  if (items.length === 0) {
    try {
      await ElMessageBox.confirm(t('ipacl.confirmClearAllow'), t('common.confirmTitle'), { type: 'warning' })
    } catch {
      return
    }
  }
  allow.saving = true
  try {
    const v = await ipApi.setAllow(items)
    allow.items = v.items.map((r) => ({ cidr: r.cidr, remark: r.remark }))
    ElMessage.success(t('common.success'))
  } catch (e) {
    if (isApiError(e)) allow.error = describeApiError(e, (k, p) => t(k, p ?? {}), (k) => te(k) || te(k, 'en-US')).join(t('shell.error.listSep'))
  } finally {
    allow.saving = false
  }
}
onMounted(() => void loadAllow())
</script>

<template>
  <div class="ga-page">
    <el-alert :title="t('ipacl.pageHint')" type="info" :closable="false" show-icon />
    <el-card>
      <el-tabs v-model="tab" data-test="ip-tabs">
        <el-tab-pane :label="t('ipacl.denyTab')" name="deny">
          <el-form class="ga-toolbar" :inline="true" @submit.prevent="table.search()">
            <el-form-item :label="t('ipacl.cidr')">
              <el-input v-model="table.query.keyword" clearable style="width: 200px" @keyup.enter="table.search()" />
            </el-form-item>
            <el-form-item>
              <el-checkbox v-model="table.query.includeExpired" @change="table.search()">{{ t('ipacl.includeExpired') }}</el-checkbox>
            </el-form-item>
            <el-form-item>
              <el-button type="primary" native-type="submit">{{ t('common.search') }}</el-button>
              <el-button @click="table.reset()">{{ t('common.reset') }}</el-button>
            </el-form-item>
            <div class="ga-toolbar__spacer" />
            <el-button v-if="canDeny" type="danger" data-test="ip-block-open" @click="blockOpen = true">{{ t('ipacl.block') }}</el-button>
          </el-form>
          <el-table v-loading="table.loading" :data="table.list" stripe data-test="ip-deny-table">
            <el-table-column :label="t('ipacl.cidr')" min-width="180">
              <template #default="{ row }"><span class="ga-mono">{{ (row as IPRule).cidr }}</span></template>
            </el-table-column>
            <el-table-column :label="t('ipacl.until')" min-width="190">
              <template #default="{ row }">
                <el-tag v-if="(row as IPRule).expiresAt === null" type="danger" size="small">{{ t('ipacl.forever') }}</el-tag>
                <span v-else :class="{ 'ga-ip__expired': expired(row as IPRule) }">
                  {{ formatTime((row as IPRule).expiresAt) }}
                  <el-tag v-if="expired(row as IPRule)" type="info" size="small">{{ t('ipacl.expired') }}</el-tag>
                </span>
              </template>
            </el-table-column>
            <el-table-column prop="remark" :label="t('common.remark')" min-width="200" show-overflow-tooltip />
            <el-table-column :label="t('common.updatedAt')" min-width="170">
              <template #default="{ row }">{{ formatTime((row as IPRule).updatedAt) }}</template>
            </el-table-column>
            <el-table-column v-if="canDeny" :label="t('common.actions')" width="100" fixed="right">
              <template #default="{ row }">
                <el-button link type="primary" data-test="ip-unblock" @click="unblock(row as IPRule)">{{ t('ipacl.unblock') }}</el-button>
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
        </el-tab-pane>
        <el-tab-pane :label="t('ipacl.allowTab')" name="allow">
          <el-alert :title="t('ipacl.allowPageHint')" type="warning" :closable="false" show-icon class="ga-ip__alert" />
          <div v-loading="allow.loading">
            <IpAllowEditor v-model="allow.items" :your-ip="allow.yourIp" :readonly="!canAllow" />
          </div>
          <el-alert v-if="allow.error" :title="allow.error" type="error" :closable="false" show-icon class="ga-ip__alert" data-test="ip-allow-error" />
          <div v-if="canAllow" class="ga-ip__footer">
            <el-button @click="loadAllow">{{ t('common.reset') }}</el-button>
            <el-button type="primary" :loading="allow.saving" data-test="ip-allow-save" @click="saveAllow">{{ t('common.save') }}</el-button>
          </div>
        </el-tab-pane>
      </el-tabs>
    </el-card>
    <IpBlockDialog v-model="blockOpen" @done="table.reload()" />
  </div>
</template>

<style scoped>
.ga-ip__expired {
  color: var(--el-text-color-secondary);
}
.ga-ip__alert {
  margin: 12px 0;
}
.ga-ip__footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 12px;
}
</style>
