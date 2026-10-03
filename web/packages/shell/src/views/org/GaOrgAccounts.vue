<script setup lang="ts">
// 主体端的子账号（D-067）：本主体的账号、角色、启停、重置密码。主账号是主体内的超管（D-061）：
// 员工改不了主账号，重置别人的密码只有主账号能做，主账号自己的密码只能由平台重置。按钮只是置灰并说明原因，挡住越权的是后端。
import { computed, onActivated, onBeforeUnmount, onDeactivated, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'
import type { FormInstance, FormRules } from 'element-plus'

import GaAvatar from '../../components/GaAvatar.vue'
import { useAuthStore } from '../../auth/store'
import { useTable } from '../../composables/useTable'
import { formatTime } from '../../format'
import { orgApi, orgPerm } from '../../org/api'
import type { OrgAccount, OrgRole } from '../../org/api'

const { t } = useI18n()
const auth = useAuthStore()
const P = {
  create: orgPerm('account:create'),
  update: orgPerm('account:update'),
  status: orgPerm('account:status'),
  assign: orgPerm('account:assign-role'),
  roles: orgPerm('role:list'),
}

interface Q extends Record<string, unknown> {
  keyword: string
  status: number | undefined
}
const table = useTable<Q, OrgAccount>({ query: { keyword: '', status: undefined }, fetch: (q) => orgApi.accounts(q) })

// 角色下拉：要有查看角色的权限才读得到；读不到时建账号就不带角色。页面缓存着（KeepAlive），每次打开对话框都重新读，
// 角色页刚建、刚启用的角色马上就能选
const roles = ref<OrgRole[]>([])
let rolesSeq = 0
async function loadRoles() {
  if (!auth.hasPerm(P.roles)) return
  const my = ++rolesSeq
  try {
    const r = await orgApi.roles()
    if (my === rolesSeq) roles.value = r
  } catch {
    // 读不到就用上次的
  }
}
void loadRoles()
const assignable = computed(() => roles.value.filter((r) => r.status === 1))

const isOwner = computed(() => auth.user?.super === true)
/** 主账号这一行：只有主账号自己能改。 */
const lockedForMe = (a: OrgAccount) => a.owner && !isOwner.value

// ---- 新建 / 编辑 ----
const dialog = reactive({ visible: false, mode: 'create' as 'create' | 'edit', id: 0, saving: false })
const form = reactive({ username: '', password: '', displayName: '', email: '', phone: '', roleIds: [] as number[], sort: 0, remark: '' })
const formRef = ref<FormInstance>()
const rules: FormRules = {
  username: [{ required: true, message: () => t('common.required'), trigger: 'blur' }],
  email: [{ type: 'email', message: () => t('err.validation.email'), trigger: 'blur' }],
}

function openCreate() {
  formSeq++
  Object.assign(form, { username: '', password: '', displayName: '', email: '', phone: '', roleIds: [], sort: 0, remark: '' })
  dialog.mode = 'create'
  dialog.id = 0
  dialog.visible = true
  void loadRoles()
}

function openEdit(a: OrgAccount) {
  formSeq++
  Object.assign(form, { username: a.username, password: '', displayName: a.displayName, email: a.email, phone: a.phone, roleIds: [], sort: a.sort, remark: a.remark })
  dialog.mode = 'edit'
  dialog.id = a.id
  dialog.visible = true
}

// ---- 一次性密码 ----
const reveal = reactive({ visible: false, username: '', password: '' })
let pageActive = true
let pageSeq = 0
let formSeq = 0
let resetSeq = 0
const resetting = ref(false)
function clearSecrets() {
  pageSeq++
  resetSeq++
  resetting.value = false
  form.password = ''
  dialog.visible = false
  dialog.saving = false
  Object.assign(reveal, { visible: false, username: '', password: '' })
}
onActivated(() => { pageActive = true })
onDeactivated(() => { pageActive = false; clearSecrets() })
onBeforeUnmount(() => { pageActive = false; clearSecrets() })
watch(() => [auth.epoch, auth.locked], clearSecrets, { flush: 'sync' })
watch(() => dialog.visible, (open) => {
  formSeq++
  if (!open) {
    form.password = ''
    dialog.saving = false
  }
}, { flush: 'sync' })
function showPassword(username: string, password: string) {
  Object.assign(reveal, { visible: true, username, password })
}
// 关掉就清空（D-098）：明文不留在页面状态和隐藏的节点里
watch(() => reveal.visible, (open) => {
  if (!open) {
    resetSeq++
    resetting.value = false
    Object.assign(reveal, { username: '', password: '' })
  }
}, { flush: 'sync' })
async function copyPassword() {
  try {
    await navigator.clipboard.writeText(reveal.password)
    ElMessage.success(t('common.copied'))
  } catch {
    // 非安全上下文没有剪贴板，用户手动复制
  }
}

async function submit() {
  if (!formRef.value || !pageActive || !dialog.visible || dialog.saving || auth.locked) return
  const target = { page: pageSeq, form: formSeq, epoch: auth.epoch, mode: dialog.mode, id: dialog.id }
  const current = () => pageActive && !auth.locked && pageSeq === target.page && formSeq === target.form && auth.epoch === target.epoch && dialog.visible && dialog.mode === target.mode && dialog.id === target.id
  dialog.saving = true
  try {
    await formRef.value.validate()
    if (!current()) return
    if (target.mode === 'create') {
      const res = await orgApi.createAccount({
        username: form.username.trim(),
        password: form.password || undefined,
        displayName: form.displayName || undefined,
        email: form.email || undefined,
        phone: form.phone || undefined,
        roleIds: form.roleIds,
        sort: form.sort,
        remark: form.remark,
      })
      if (!current()) return
      dialog.visible = false
      ElMessage.success(t('common.success'))
      if (res.initialPassword) showPassword(res.account.username, res.initialPassword)
    } else {
      await orgApi.updateAccount(target.id, { displayName: form.displayName, email: form.email, phone: form.phone, sort: form.sort, remark: form.remark })
      if (!current()) return
      dialog.visible = false
      ElMessage.success(t('common.success'))
    }
    await table.reload()
  } catch {
    // 已提示
  } finally {
    if (current()) dialog.saving = false
  }
}

async function toggleStatus(a: OrgAccount) {
  const next = a.status === 1 ? 0 : 1
  try {
    await ElMessageBox.confirm(t(next === 1 ? 'org.account.confirmEnable' : 'org.account.confirmDisable', { name: a.username }), t('common.confirmTitle'), { type: 'warning' })
  } catch {
    return
  }
  await orgApi.setAccountStatus(a.id, next)
  ElMessage.success(t('common.success'))
  await table.reload()
}

async function resetPassword(a: OrgAccount) {
  if (!pageActive || resetting.value || auth.locked) return
  const target = { page: pageSeq, epoch: auth.epoch, id: a.id, username: a.username }
  const my = ++resetSeq
  const current = () => pageActive && !auth.locked && pageSeq === target.page && auth.epoch === target.epoch && my === resetSeq
  resetting.value = true
  try {
    await ElMessageBox.confirm(t('org.account.confirmReset', { name: target.username }), t('common.confirmTitle'), { type: 'warning' })
    if (!current()) return
    const res = await orgApi.resetPassword(target.id)
    if (!current()) return
    showPassword(target.username, res.initialPassword)
    await table.reload()
  } catch {
    // 取消或失败由请求层提示，失去页面身份的结果不展示。
  } finally {
    if (my === resetSeq) resetting.value = false
  }
}

const roleDialog = reactive({ visible: false, id: 0, username: '', roleIds: [] as number[], held: [] as { id: number; name: string }[], saving: false })
function openRoles(a: OrgAccount) {
  Object.assign(roleDialog, { visible: true, id: a.id, username: a.username, roleIds: a.roles.map((r) => r.id), held: a.roles.map((r) => ({ id: r.id, name: r.name })) })
  void loadRoles()
}
/** 分配角色的选项：启用的角色，加上这个账号已经有的（停用了也列出来，显示名字而不是编号）。 */
const roleOptions = computed(() => {
  const out = assignable.value.map((r) => ({ id: r.id, name: r.name }))
  for (const h of roleDialog.held) if (!out.some((o) => o.id === h.id)) out.push(h)
  return out
})
async function saveRoles() {
  roleDialog.saving = true
  try {
    await orgApi.assignRoles(roleDialog.id, roleDialog.roleIds)
    roleDialog.visible = false
    ElMessage.success(t('common.success'))
    await table.reload()
  } catch {
    // 已提示
  } finally {
    roleDialog.saving = false
  }
}
</script>

<template>
  <div class="ga-page">
    <el-card>
      <el-form class="ga-toolbar" :inline="true" @submit.prevent="table.search()">
        <el-form-item :label="t('common.keyword')">
          <el-input v-model="table.query.keyword" clearable :placeholder="t('org.account.keywordPlaceholder')" style="width: 200px" @keyup.enter="table.search()" />
        </el-form-item>
        <el-form-item :label="t('common.status')">
          <el-select v-model="table.query.status" clearable :placeholder="t('common.all')" style="width: 120px">
            <el-option :value="1" :label="t('common.enabled')" />
            <el-option :value="0" :label="t('common.disabled')" />
          </el-select>
        </el-form-item>
        <el-form-item>
          <el-button type="primary" native-type="submit">{{ t('common.search') }}</el-button>
          <el-button @click="table.reset()">{{ t('common.reset') }}</el-button>
        </el-form-item>
        <div class="ga-toolbar__spacer" />
        <el-button v-perm="P.create" type="primary" data-test="org-account-create" @click="openCreate">{{ t('common.create') }}</el-button>
      </el-form>
    </el-card>

    <el-card>
      <el-table v-loading="table.loading" :data="table.list" stripe data-test="org-account-table">
        <el-table-column prop="id" :label="t('common.id')" width="70" />
        <el-table-column :label="t('common.username')" min-width="200">
          <template #default="{ row }">
            <span class="ga-org-account__who">
              <GaAvatar :size="26" :value="(row as OrgAccount).avatar" :name="(row as OrgAccount).username" />
              <span>{{ (row as OrgAccount).username }}</span>
              <el-tag v-if="(row as OrgAccount).owner" type="warning" size="small">{{ t('org.account.owner') }}</el-tag>
            </span>
          </template>
        </el-table-column>
        <el-table-column prop="displayName" :label="t('common.displayName')" min-width="120" />
        <el-table-column :label="t('org.account.roles')" min-width="160">
          <template #default="{ row }">
            <el-tag v-for="r in (row as OrgAccount).roles" :key="r.id" size="small" class="ga-tag">{{ r.name }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column :label="t('common.status')" min-width="90">
          <template #default="{ row }">
            <el-tag :type="(row as OrgAccount).status === 1 ? 'success' : 'info'" size="small">{{ (row as OrgAccount).status === 1 ? t('common.enabled') : t('common.disabled') }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column :label="t('org.account.lastLogin')" min-width="170">
          <template #default="{ row }">{{ formatTime((row as OrgAccount).lastLoginAt) }}</template>
        </el-table-column>
        <el-table-column :label="t('common.createdAt')" min-width="170">
          <template #default="{ row }">{{ formatTime((row as OrgAccount).createdAt) }}</template>
        </el-table-column>
        <el-table-column :label="t('common.actions')" width="330" fixed="right">
          <template #default="{ row }">
            <el-tooltip :disabled="!lockedForMe(row as OrgAccount)" :content="t('org.account.ownerProtectedHint')" placement="top">
              <span :data-test="`org-account-actions-${(row as OrgAccount).username}`">
                <el-button v-perm="P.update" link type="primary" :disabled="lockedForMe(row as OrgAccount)" @click="openEdit(row as OrgAccount)">{{ t('common.edit') }}</el-button>
                <el-button v-perm="P.assign" link type="primary" :disabled="lockedForMe(row as OrgAccount)" data-test="org-account-roles" @click="openRoles(row as OrgAccount)">
                  {{ t('org.account.assignRoles') }}
                </el-button>
                <el-button
                  v-if="isOwner"
                  link
                  type="warning"
                  :disabled="(row as OrgAccount).owner || resetting"
                  :title="(row as OrgAccount).owner ? t('org.account.resetOwnerHint') : ''"
                  data-test="org-account-reset"
                  @click="resetPassword(row as OrgAccount)"
                >
                  {{ t('org.account.resetPassword') }}
                </el-button>
                <el-button
                  v-perm="P.status"
                  link
                  :type="(row as OrgAccount).status === 1 ? 'danger' : 'success'"
                  :disabled="(row as OrgAccount).owner || (row as OrgAccount).id === auth.user?.id"
                  @click="toggleStatus(row as OrgAccount)"
                >
                  {{ (row as OrgAccount).status === 1 ? t('common.disable') : t('common.enable') }}
                </el-button>
              </span>
            </el-tooltip>
          </template>
        </el-table-column>
      </el-table>
      <div class="ga-pagination">
        <el-pagination
          v-model:current-page="table.page"
          v-model:page-size="table.pageSize"
          :total="table.total"
          :page-sizes="[10, 20, 50, 100]"
          layout="total, sizes, prev, pager, next"
          @current-change="table.onPageChange"
          @size-change="table.onSizeChange"
        />
      </div>
    </el-card>

    <el-dialog v-model="dialog.visible" :title="dialog.mode === 'create' ? t('org.account.createTitle') : t('org.account.editTitle')" width="520px" destroy-on-close>
      <el-form ref="formRef" :model="form" :rules="rules" label-width="auto" @submit.prevent="submit">
        <el-form-item prop="username" :label="t('common.username')">
          <el-input v-model="form.username" :disabled="dialog.mode === 'edit'" maxlength="64" data-test="org-account-form-username" />
        </el-form-item>
        <el-form-item v-if="dialog.mode === 'create'" prop="password" :label="t('org.account.password')">
          <el-input v-model="form.password" type="password" show-password maxlength="72" autocomplete="new-password" data-test="org-account-form-password" />
          <div class="ga-form-hint">{{ t('org.account.passwordHint') }}</div>
        </el-form-item>
        <el-form-item prop="displayName" :label="t('common.displayName')">
          <el-input v-model="form.displayName" maxlength="64" />
        </el-form-item>
        <el-form-item prop="email" :label="t('org.account.email')">
          <el-input v-model="form.email" maxlength="128" />
        </el-form-item>
        <el-form-item prop="phone" :label="t('org.account.phone')">
          <el-input v-model="form.phone" maxlength="32" />
        </el-form-item>
        <el-form-item v-if="dialog.mode === 'create' && auth.hasPerm(P.assign) && assignable.length" prop="roleIds" :label="t('org.account.roles')">
          <el-select v-model="form.roleIds" multiple style="width: 100%" data-test="org-account-form-roles">
            <el-option v-for="r in assignable" :key="r.id" :value="r.id" :label="r.name" />
          </el-select>
        </el-form-item>
        <el-form-item prop="sort" :label="t('common.sort')">
          <el-input-number v-model="form.sort" :min="0" :max="1000000" />
        </el-form-item>
        <el-form-item prop="remark" :label="t('common.remark')">
          <el-input v-model="form.remark" type="textarea" maxlength="255" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialog.visible = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :loading="dialog.saving" data-test="org-account-form-submit" @click="submit">{{ t('common.save') }}</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="roleDialog.visible" :title="t('org.account.assignRolesTitle', { name: roleDialog.username })" width="420px">
      <el-select v-model="roleDialog.roleIds" multiple style="width: 100%" data-test="org-account-roles-select">
        <el-option v-for="r in roleOptions" :key="r.id" :value="r.id" :label="r.name" />
      </el-select>
      <template #footer>
        <el-button @click="roleDialog.visible = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :loading="roleDialog.saving" data-test="org-account-roles-save" @click="saveRoles">{{ t('common.save') }}</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="reveal.visible" :title="t('org.account.initialPasswordTitle')" width="420px" destroy-on-close data-test="org-password-reveal">
      <el-alert :title="t('org.account.initialPasswordHint')" type="warning" :closable="false" show-icon />
      <el-descriptions :column="1" border style="margin-top: 12px">
        <el-descriptions-item :label="t('common.username')">{{ reveal.username }}</el-descriptions-item>
        <el-descriptions-item :label="t('org.account.password')">
          <span class="ga-mono" data-test="org-initial-password">{{ reveal.password }}</span>
          <el-button link type="primary" style="margin-left: 8px" @click="copyPassword">{{ t('common.copy') }}</el-button>
        </el-descriptions-item>
      </el-descriptions>
      <template #footer>
        <el-button type="primary" data-test="org-password-close" @click="reveal.visible = false">{{ t('common.close') }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.ga-org-account__who {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}
</style>
