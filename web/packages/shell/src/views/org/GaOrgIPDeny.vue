<script setup lang="ts">
import { reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useTable } from '../../composables/useTable'
import { formatTime } from '../../format'
import { describeApiError } from '../../request/errors'
import { isApiError } from '../../types'
import { orgApi } from '../../org/api'
import type { OrgIPRule } from '../../org/api'

const { t, te } = useI18n()
const table = useTable<{ keyword: string; includeExpired: number }, OrgIPRule>({ query: { keyword: '', includeExpired: 0 }, fetch: (p) => orgApi.denyList(p) })
const open = ref(false)
const saving = ref(false)
const editing = ref(false)
const error = ref('')
const form = reactive({ cidr: '', expiresIn: 1440, remark: '' })
const durations = [60, 1440, 10080, 43200, 525600, 0]
const expired = (row: OrgIPRule) => row.expiresAt !== null && Date.parse(row.expiresAt) <= Date.now()
function edit(row?: OrgIPRule) {
  editing.value = !!row
  Object.assign(form, { cidr: row?.cidr ?? '', expiresIn: 1440, remark: row?.remark ?? '' })
  error.value = ''
  open.value = true
}
function durationLabel(m: number) {
  return m === 0 ? t('ipacl.forever') : m >= 1440 ? t('ipacl.days', { n: m / 1440 }) : t('ipacl.hours', { n: m / 60 })
}
async function save() {
  if (saving.value) return
  error.value = ''
  if (!form.cidr.trim()) { error.value = t('common.required'); return }
  saving.value = true
  try {
    await orgApi.addDeny({ cidr: form.cidr.trim(), expiresIn: form.expiresIn, remark: form.remark.trim() })
    open.value = false
    ElMessage.success(t('common.success'))
    await table.reload()
  } catch (e) {
    if (isApiError(e)) error.value = describeApiError(e, (k, p) => t(k, p ?? {}), (k) => te(k) || te(k, 'en-US')).join(t('shell.error.listSep'))
  } finally { saving.value = false }
}
async function remove(row: OrgIPRule) {
  try { await ElMessageBox.confirm(t('ipacl.confirmUnblock', { cidr: row.cidr }), t('common.confirmTitle'), { type: 'warning' }) } catch { return }
  await orgApi.removeDeny(row.id)
  ElMessage.success(t('common.success'))
  await table.reload()
}
</script>

<template>
  <div class="ga-page" data-test="org-ip-deny">
    <el-alert :title="t('org.denyHint')" type="info" :closable="false" show-icon />
    <el-card>
      <el-form inline @submit.prevent="table.search()">
        <el-form-item :label="t('ipacl.cidr')"><el-input v-model="table.query.keyword" clearable maxlength="64" /></el-form-item>
        <el-form-item><el-checkbox v-model="table.query.includeExpired" :true-value="1" :false-value="0" @change="table.search()">{{ t('ipacl.includeExpired') }}</el-checkbox></el-form-item>
        <el-form-item><el-button native-type="submit">{{ t('common.search') }}</el-button></el-form-item>
        <el-form-item><el-button type="danger" data-test="org-deny-add" @click="edit()">{{ t('ipacl.block') }}</el-button></el-form-item>
      </el-form>
      <el-table v-loading="table.loading" :data="table.list" stripe data-test="org-deny-table">
        <el-table-column prop="cidr" :label="t('ipacl.cidr')" min-width="180" />
        <el-table-column :label="t('ipacl.until')" min-width="180">
          <template #default="{ row }">
            <el-tag v-if="row.expiresAt === null" type="danger" size="small">{{ t('ipacl.forever') }}</el-tag>
            <template v-else>{{ formatTime(row.expiresAt) }} <el-tag v-if="expired(row as OrgIPRule)" type="info" size="small">{{ t('ipacl.expired') }}</el-tag></template>
          </template>
        </el-table-column>
        <el-table-column prop="remark" :label="t('common.remark')" min-width="160" />
        <el-table-column :label="t('common.actions')" width="160" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="edit(row as OrgIPRule)">{{ t('common.edit') }}</el-button>
            <el-button link type="danger" data-test="org-deny-remove" @click="remove(row as OrgIPRule)">{{ t('ipacl.unblock') }}</el-button>
          </template>
        </el-table-column>
      </el-table>
      <div class="ga-pagination">
        <el-pagination v-model:current-page="table.page" v-model:page-size="table.pageSize" :total="table.total" :page-sizes="[20, 50, 100]" layout="total, sizes, prev, pager, next" @current-change="table.onPageChange" @size-change="table.onSizeChange" />
      </div>
    </el-card>
    <el-dialog v-model="open" :title="t('ipacl.blockTitle')" width="min(480px, 94vw)" :close-on-click-modal="!saving">
      <p class="ga-form-hint">{{ t('ipacl.wildcardHint') }}</p>
      <el-form label-width="100px" @submit.prevent="save">
        <el-form-item :label="t('ipacl.cidr')" required><el-input v-model="form.cidr" :disabled="editing" maxlength="64" :placeholder="t('ipacl.cidrPlaceholder')" data-test="org-deny-cidr" /></el-form-item>
        <el-form-item :label="t('ipacl.duration')"><el-select v-model="form.expiresIn"><el-option v-for="m in durations" :key="m" :value="m" :label="durationLabel(m)" /></el-select></el-form-item>
        <el-form-item :label="t('common.remark')"><el-input v-model="form.remark" maxlength="255" /></el-form-item>
      </el-form>
      <el-alert v-if="error" :title="error" type="error" :closable="false" show-icon />
      <template #footer>
        <el-button :disabled="saving" @click="open = false">{{ t('common.cancel') }}</el-button>
        <el-button type="danger" :loading="saving" data-test="org-deny-save" @click="save">{{ t('common.save') }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>
