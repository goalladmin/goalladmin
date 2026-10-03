<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessageBox } from 'element-plus'
import { formatTime, useAuthStore, useI18n, useTable } from '@ga/shell'
import { invitationsApi } from '../../api/onboarding'
const { t } = useI18n()
const auth = useAuthStore()
const table = useTable({ query: {}, immediate: false, fetch: invitationsApi.list })
const url = ref('')
const busy = ref(false)
const enabled = ref(false)
onMounted(async () => {
  if (!auth.user?.super) return
  try { enabled.value = (await invitationsApi.config()).invitationsEnabled } catch { /* 保持禁用，接口已提示。 */ }
})
if (auth.user?.super) void table.reload()
async function create() {
  if (!enabled.value || busy.value) return
  busy.value = true
  try { url.value = (await invitationsApi.create()).url; await table.reload() }
  catch { /* 接口已经提示。 */ } finally { busy.value = false }
}
async function revoke(id: number) {
  try { await ElMessageBox.confirm(t('onboarding.revoke'), t('onboarding.invitations')); await invitationsApi.revoke(id); await table.reload() }
  catch { /* 取消或接口已经提示。 */ }
}
</script>
<template>
  <div class="ga-page">
    <el-alert v-if="!auth.user?.super" :title="t('onboarding.ownerOnly')" type="info" :closable="false" />
    <el-card v-else>
      <p>{{ t('onboarding.inviteExplain') }}</p>
      <el-alert v-if="!enabled" :title="t('onboarding.closed')" type="info" :closable="false" />
      <el-button type="primary" :loading="busy" :disabled="!enabled" @click="create">{{ t('onboarding.createInvite') }}</el-button>
      <el-table v-loading="table.loading" :data="table.list">
        <el-table-column prop="id" label="ID" />
        <el-table-column :label="t('onboarding.createdAt')"><template #default="{ row }">{{ formatTime(row.createdAt) }}</template></el-table-column>
        <el-table-column :label="t('onboarding.expiresAt')"><template #default="{ row }">{{ formatTime(row.expiresAt) }}</template></el-table-column>
        <el-table-column :label="t('common.status')"><template #default="{ row }">{{ t(row.usedAt ? 'onboarding.used' : row.revokedAt ? 'onboarding.revoked' : Date.parse(row.expiresAt) <= Date.now() ? 'onboarding.expired' : 'onboarding.active') }}</template></el-table-column>
        <el-table-column><template #default="{ row }"><el-button v-if="!row.usedAt && !row.revokedAt" text @click="revoke(row.id)">{{ t('onboarding.revoke') }}</el-button></template></el-table-column>
      </el-table>
      <el-pagination v-model:current-page="table.page" :page-size="table.pageSize" :total="table.total" layout="total, prev, pager, next" @current-change="table.onPageChange" />
    </el-card>
    <el-dialog :model-value="!!url" :title="t('onboarding.createInvite')" :close-on-click-modal="false" @close="url = ''"><p>{{ t('onboarding.inviteOnce') }}</p><el-input :model-value="url" readonly /></el-dialog>
  </div>
</template>
