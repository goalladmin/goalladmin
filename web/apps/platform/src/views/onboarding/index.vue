<script setup lang="ts">
import { ref } from 'vue'
import { ElMessageBox } from 'element-plus'
import { formatTime, useI18n, useTable } from '@ga/shell'
import { onboardingApi } from '../../api/onboarding'
import type { Application, Approval } from '../../api/onboarding'
const { t } = useI18n()
const table = useTable({ query: { state: 'pending', keyword: '' }, fetch: onboardingApi.list })
const busy = ref(false)
const credentials = ref<Approval | null>(null)
async function review(row: Application, decision: 'approve' | 'reject') {
  if (busy.value) return
  try {
    const { value } = await ElMessageBox.prompt(t(decision === 'approve' ? 'onboarding.confirmApprove' : 'onboarding.note'), t('onboarding.title'), { inputValidator: v => decision === 'approve' || !!v?.trim(), inputErrorMessage: t('onboarding.note') })
    busy.value = true
    credentials.value = await onboardingApi.review(row.id, decision, value ?? '')
    await table.reload()
  } catch { /* 取消或接口已经提示。 */ } finally { busy.value = false }
}
</script>
<template>
  <div class="ga-page">
    <el-card>
      <el-form inline @submit.prevent="table.search()">
        <el-form-item><el-input v-model="table.query.keyword" :placeholder="t('onboarding.reference')" /></el-form-item>
        <el-form-item><el-select v-model="table.query.state" style="width: 150px"><el-option v-for="state in ['', 'pending', 'approved', 'rejected']" :key="state" :value="state" :label="t(state ? `onboarding.${state}` : 'common.all')" /></el-select></el-form-item>
        <el-button native-type="submit">{{ t('common.search') }}</el-button>
      </el-form>
      <el-table v-loading="table.loading" :data="table.list" data-test="applications-table">
        <el-table-column prop="reference" :label="t('onboarding.reference')" min-width="180" />
        <el-table-column prop="portal" :label="t('onboarding.portal')" width="110" />
        <el-table-column prop="name" :label="t('onboarding.name')" min-width="140" />
        <el-table-column prop="contactName" :label="t('onboarding.contactName')" min-width="120" />
        <el-table-column prop="contactPhone" :label="t('onboarding.contactPhone')" min-width="140" />
        <el-table-column prop="ownerUsername" :label="t('onboarding.username')" min-width="120" />
        <el-table-column prop="agentId" :label="t('onboarding.agent')" width="100" />
        <el-table-column :label="t('common.status')" width="110"><template #default="{ row }">{{ t(`onboarding.${row.reviewState}`) }}</template></el-table-column>
        <el-table-column prop="orgCode" :label="t('onboarding.orgCode')" min-width="120" />
        <el-table-column prop="reviewNote" :label="t('onboarding.note')" min-width="140" />
        <el-table-column :label="t('onboarding.createdAt')" min-width="170"><template #default="{ row }">{{ formatTime(row.createdAt) }}</template></el-table-column>
        <el-table-column fixed="right" width="170"><template #default="{ row }"><template v-if="row.reviewState === 'pending'"><el-button v-perm="'onboarding:application:review'" :disabled="busy" size="small" @click="review(row as Application, 'approve')">{{ t('onboarding.approve') }}</el-button><el-button v-perm="'onboarding:application:review'" :disabled="busy" size="small" @click="review(row as Application, 'reject')">{{ t('onboarding.reject') }}</el-button></template></template></el-table-column>
      </el-table>
      <el-pagination v-model:current-page="table.page" :page-size="table.pageSize" :total="table.total" layout="total, prev, pager, next" @current-change="table.onPageChange" />
    </el-card>
    <el-dialog :model-value="!!credentials" :title="t('onboarding.approved')" :close-on-click-modal="false" @close="credentials = null">
      <template v-if="credentials"><el-alert :title="t('onboarding.once')" type="warning" :closable="false" /><p>{{ credentials.org.code }} / {{ credentials.org.ownerUsername }}</p><el-input :model-value="credentials.password" readonly /></template>
    </el-dialog>
  </div>
</template>
