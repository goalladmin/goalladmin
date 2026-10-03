<script setup lang="ts">
// 主体端的角色与权限（D-067）：角色只属于本主体（D-063）。主账号能授任何权限；员工只能授出自己拥有的、
// 不带"敏感"标记的权限码。界面上直接置灰，挡住越权的是后端。主体端没有部门，也就没有数据范围。
import { reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'
import type { FormInstance, FormRules } from 'element-plus'

import { useAuthStore } from '../../auth/store'
import { formatTime } from '../../format'
import { orgApi, orgPerm } from '../../org/api'
import type { OrgPermGroup, OrgRole, OrgRoleInput } from '../../org/api'

const { t, te } = useI18n()
const auth = useAuthStore()
const P = { create: orgPerm('role:create'), update: orgPerm('role:update'), remove: orgPerm('role:delete'), grant: orgPerm('role:grant') }

const roles = ref<OrgRole[]>([])
const loading = ref(false)
async function load() {
  loading.value = true
  try {
    roles.value = await orgApi.roles()
  } catch {
    // 已提示
  } finally {
    loading.value = false
  }
}
void load()

// ---- 新建 / 编辑 ----
const dialog = reactive({ visible: false, id: 0, saving: false })
const form = reactive<OrgRoleInput>({ code: '', name: '', status: 1, sort: 0, remark: '' })
const formRef = ref<FormInstance>()
const rules: FormRules = {
  code: [
    { required: true, message: () => t('common.required'), trigger: 'blur' },
    { pattern: /^[a-z][a-z0-9_-]*$/, message: () => t('org.role.codeRule'), trigger: 'blur' },
  ],
  name: [{ required: true, message: () => t('common.required'), trigger: 'blur' }],
}

function openCreate() {
  dialog.id = 0
  Object.assign(form, { code: '', name: '', status: 1, sort: 0, remark: '' })
  dialog.visible = true
}

function openEdit(r: OrgRole) {
  dialog.id = r.id
  Object.assign(form, { code: r.code, name: r.name, status: r.status, sort: r.sort, remark: r.remark })
  dialog.visible = true
}

async function submit() {
  if (!formRef.value) return
  try {
    await formRef.value.validate()
  } catch {
    return
  }
  dialog.saving = true
  try {
    if (dialog.id === 0) await orgApi.createRole({ ...form })
    else await orgApi.updateRole(dialog.id, { ...form })
    dialog.visible = false
    ElMessage.success(t('common.success'))
    await load()
  } catch {
    // 已提示
  } finally {
    dialog.saving = false
  }
}

async function remove(r: OrgRole) {
  try {
    await ElMessageBox.confirm(t('common.confirmDelete'), t('common.confirmTitle'), { type: 'warning' })
  } catch {
    return
  }
  await orgApi.removeRole(r.id)
  ElMessage.success(t('common.success'))
  await load()
}

// ---- 授权 ----
// loaded：这个角色的授权已经完整读回来。没读完（或读失败）时不能保存，否则会把空表单或上一个角色的勾选提交给它（D-046）
const grant = reactive({ visible: false, role: null as OrgRole | null, saving: false, loading: false, loaded: false })
const tree = ref<OrgPermGroup[]>([])
const checked = ref(new Set<string>())

function permName(name: string) {
  return te(name) ? t(name) : name
}
function groupTitle(g: OrgPermGroup) {
  return te(`permGroup.${g.group}`) ? t(`permGroup.${g.group}`) : g.group
}

/** 员工不能授出敏感权限码，也不能授出自己没有的（D-063）。 */
function disabled(p: { code: string; sensitive: boolean }) {
  if (auth.user?.super) return false
  if (p.sensitive) return true
  return !auth.hasPerm(p.code)
}

let grantSeq = 0
async function openGrant(r: OrgRole) {
  const my = ++grantSeq
  Object.assign(grant, { role: r, visible: true, loading: true, loaded: false })
  tree.value = []
  checked.value = new Set()
  try {
    const [groups, codes] = await Promise.all([orgApi.permTree(), orgApi.rolePerms(r.id)])
    if (my !== grantSeq) return
    tree.value = groups
    checked.value = new Set(codes)
    grant.loaded = true
  } catch {
    // 已提示；没读完就不能保存
  } finally {
    if (my === grantSeq) grant.loading = false
  }
}

function toggle(code: string, on: boolean) {
  const next = new Set(checked.value)
  if (on) next.add(code)
  else next.delete(code)
  checked.value = next
}

function groupState(g: OrgPermGroup) {
  const enabled = g.perms.filter((p) => !disabled(p))
  const n = enabled.filter((p) => checked.value.has(p.code)).length
  return { all: enabled.length > 0 && n === enabled.length, some: n > 0 && n < enabled.length, enabled }
}

function toggleGroup(g: OrgPermGroup, on: boolean) {
  const next = new Set(checked.value)
  for (const p of g.perms) {
    if (disabled(p)) continue
    if (on) next.add(p.code)
    else next.delete(p.code)
  }
  checked.value = next
}

async function saveGrant() {
  if (!grant.role || !grant.loaded || grant.loading) return
  grant.saving = true
  try {
    await orgApi.grant(grant.role.id, [...checked.value])
    grant.visible = false
    ElMessage.success(t('common.success'))
  } catch {
    // 已提示
  } finally {
    grant.saving = false
  }
}
</script>

<template>
  <div class="ga-page">
    <el-card>
      <div class="ga-toolbar">
        <el-button @click="load">{{ t('common.reset') }}</el-button>
        <div class="ga-toolbar__spacer" />
        <el-button v-perm="P.create" type="primary" data-test="org-role-create" @click="openCreate">{{ t('common.create') }}</el-button>
      </div>
    </el-card>
    <el-card>
      <el-table v-loading="loading" :data="roles" stripe data-test="org-role-table">
        <el-table-column prop="id" :label="t('common.id')" width="70" />
        <el-table-column prop="code" :label="t('org.role.code')" min-width="120" />
        <el-table-column prop="name" :label="t('org.role.name')" min-width="140" />
        <el-table-column :label="t('common.status')" min-width="90">
          <template #default="{ row }">
            <el-tag :type="(row as OrgRole).status === 1 ? 'success' : 'info'" size="small">{{ (row as OrgRole).status === 1 ? t('common.enabled') : t('common.disabled') }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="sort" :label="t('common.sort')" min-width="90" />
        <el-table-column prop="remark" :label="t('common.remark')" min-width="140" show-overflow-tooltip />
        <el-table-column :label="t('common.createdAt')" min-width="170">
          <template #default="{ row }">{{ formatTime((row as OrgRole).createdAt) }}</template>
        </el-table-column>
        <el-table-column :label="t('common.actions')" width="220" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="P.update" link type="primary" @click="openEdit(row as OrgRole)">{{ t('common.edit') }}</el-button>
            <el-button v-perm="P.grant" link type="primary" data-test="org-role-grant" @click="openGrant(row as OrgRole)">{{ t('org.role.grant') }}</el-button>
            <el-button v-perm="P.remove" link type="danger" @click="remove(row as OrgRole)">{{ t('common.delete') }}</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog v-model="dialog.visible" :title="dialog.id === 0 ? t('org.role.createTitle') : t('org.role.editTitle')" width="480px" destroy-on-close>
      <el-form ref="formRef" :model="form" :rules="rules" label-width="auto" @submit.prevent="submit">
        <el-form-item prop="code" :label="t('org.role.code')">
          <el-input v-model="form.code" :disabled="dialog.id !== 0" maxlength="64" data-test="org-role-form-code" />
          <div v-if="dialog.id === 0" class="ga-form-hint">{{ t('org.role.codeRule') }}</div>
        </el-form-item>
        <el-form-item prop="name" :label="t('org.role.name')">
          <el-input v-model="form.name" maxlength="64" data-test="org-role-form-name" />
        </el-form-item>
        <el-form-item prop="status" :label="t('common.status')">
          <el-switch v-model="form.status" :active-value="1" :inactive-value="0" />
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
        <el-button type="primary" :loading="dialog.saving" data-test="org-role-form-submit" @click="submit">{{ t('common.save') }}</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="grant.visible" :title="t('org.role.grantTitle', { name: grant.role?.name ?? '' })" width="640px" data-test="org-grant-dialog">
      <el-alert v-if="!auth.user?.super" :title="t('org.role.grantLimit')" type="info" :closable="false" show-icon style="margin-bottom: 12px" />
      <div v-loading="grant.loading" class="ga-org-perms">
        <div v-for="g in tree" :key="g.group" class="ga-org-perms__group">
          <el-checkbox
            :model-value="groupState(g).all"
            :indeterminate="groupState(g).some"
            :disabled="groupState(g).enabled.length === 0"
            class="ga-org-perms__title"
            @change="(v: string | number | boolean) => toggleGroup(g, v === true)"
          >
            {{ groupTitle(g) }}
          </el-checkbox>
          <div class="ga-org-perms__items">
            <el-checkbox
              v-for="p in g.perms"
              :key="p.code"
              :model-value="checked.has(p.code)"
              :disabled="disabled(p)"
              :data-test="`org-perm-${p.code}`"
              @change="(v: string | number | boolean) => toggle(p.code, v === true)"
            >
              {{ permName(p.name) }}
              <el-tag v-if="p.sensitive" type="danger" size="small" effect="plain" style="margin-left: 4px">{{ t('org.role.sensitive') }}</el-tag>
              <span class="ga-org-perms__code">{{ p.code }}</span>
            </el-checkbox>
          </div>
        </div>
      </div>
      <template #footer>
        <el-button @click="grant.visible = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :loading="grant.saving" :disabled="!grant.loaded || grant.loading" data-test="org-grant-submit" @click="saveGrant">{{ t('common.save') }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.ga-org-perms {
  max-height: 60vh;
  overflow: auto;
}

.ga-org-perms__group {
  border: 1px solid var(--ga-border);
  border-radius: var(--ga-radius);
  padding: 8px 12px;
  margin-bottom: 10px;
}

.ga-org-perms__title {
  font-weight: 600;
}

.ga-org-perms__items {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 20px;
  padding: 6px 0 0 22px;
}

.ga-org-perms__code {
  margin-left: 6px;
  color: var(--ga-text-secondary);
  font-size: 12px;
}
</style>
