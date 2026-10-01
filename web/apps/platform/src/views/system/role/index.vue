<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import type { FormInstance, FormRules } from 'element-plus'
import { formatTime, useAuthStore, useI18n } from '@ga/shell'

import { roleApi } from '../../../api/system'
import type { DataResource, DataScope, PermGroup, Role, RoleInput } from '../../../api/system'

const { t, te } = useI18n()
const auth = useAuthStore()

// ---- 列表 ----
const roles = ref<Role[]>([])
const loading = ref(false)
async function load() {
  loading.value = true
  try {
    roles.value = await roleApi.list()
  } finally {
    loading.value = false
  }
}
void load()

// ---- 新建 / 编辑 ----
const dialog = reactive({ visible: false, id: 0, saving: false })
const form = reactive<RoleInput>({ code: '', name: '', status: 1, sort: 0, remark: '' })
const formRef = ref<FormInstance>()
const rules: FormRules = {
  code: [
    { required: true, message: () => t('common.required'), trigger: 'blur' },
    { pattern: /^[a-z][a-z0-9-]*$/, message: () => t('sys.role.codeRule'), trigger: 'blur' },
  ],
  name: [{ required: true, message: () => t('common.required'), trigger: 'blur' }],
}

function openCreate() {
  dialog.id = 0
  Object.assign(form, { code: '', name: '', status: 1, sort: 0, remark: '' })
  dialog.visible = true
}

function openEdit(r: Role) {
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
    if (dialog.id === 0) await roleApi.create(form)
    else await roleApi.update(dialog.id, { name: form.name, status: form.status, sort: form.sort, remark: form.remark })
    dialog.visible = false
    ElMessage.success(t('common.success'))
    await load()
  } catch {
    // 已提示
  } finally {
    dialog.saving = false
  }
}

async function remove(r: Role) {
  try {
    await ElMessageBox.confirm(t('common.confirmDelete'), t('common.confirmTitle'), { type: 'warning' })
  } catch {
    return
  }
  await roleApi.remove(r.id)
  ElMessage.success(t('common.success'))
  await load()
}

// ---- 授权 ----
// loaded：这个角色的授权已经完整读回来。没读完（或读失败）时不能保存——否则会把空表单或上一个角色的勾选提交给它（D-046）
const grant = reactive({ visible: false, role: null as Role | null, saving: false, loading: false, loaded: false, tab: 'perms' })
const tree = ref<PermGroup[]>([])
const checked = ref(new Set<string>())
// 数据权限（D-039）：每个数据资源选一个范围，和功能权限一起保存
const resources = ref<DataResource[]>([])
const scopes = ref<Record<string, DataScope>>({})
let loadedScopes: Record<string, DataScope> = {}

const scopeLabel: Record<DataScope, string> = {
  self: 'sys.role.scopeSelf',
  dept: 'sys.role.scopeDept',
  dept_tree: 'sys.role.scopeDeptTree',
  all: 'sys.role.scopeAll',
}

function permName(code: string, name: string) {
  return te(name) ? t(name) : name
}

/** 非超管不能授出敏感权限码，也不能授出自己没有的（规范 §6.5）；界面上直接禁用。 */
function disabled(p: { code: string; sensitive: boolean }) {
  if (auth.user?.super) return false
  if (p.sensitive) return true
  return !auth.hasPerm(p.code)
}

// 打开序号：连续打开两个角色时，前一次的慢响应不能盖住当前这个角色的表单
let grantSeq = 0
async function openGrant(r: Role) {
  const my = ++grantSeq
  grant.role = r
  grant.visible = true
  grant.loading = true
  grant.loaded = false
  grant.tab = 'perms'
  tree.value = []
  checked.value = new Set()
  resources.value = []
  scopes.value = {}
  loadedScopes = {}
  try {
    const [groups, codes, res, sc] = await Promise.all([roleApi.permTree(), roleApi.perms(r.id), roleApi.dataResources(), roleApi.dataScopes(r.id)])
    if (my !== grantSeq) return
    tree.value = groups
    checked.value = new Set(codes)
    resources.value = res
    scopes.value = { ...sc }
    loadedScopes = { ...sc }
    grant.loaded = true
  } catch {
    // 已由请求层提示；没读完就不能保存
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

function groupState(g: PermGroup) {
  const enabled = g.perms.filter((p) => !disabled(p))
  const n = enabled.filter((p) => checked.value.has(p.code)).length
  return { all: enabled.length > 0 && n === enabled.length, some: n > 0 && n < enabled.length, enabled }
}

function toggleGroup(g: PermGroup, on: boolean) {
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
    // 只提交改过的范围：没动的资源继续跟着代码里的默认值走，也不会覆盖别人同时做的修改
    const changed: Record<string, DataScope> = {}
    for (const [code, scope] of Object.entries(scopes.value)) {
      if (loadedScopes[code] !== scope) changed[code] = scope
    }
    await roleApi.grant(grant.role.id, [...checked.value], Object.keys(changed).length ? changed : undefined)
    grant.visible = false
    ElMessage.success(t('common.success'))
  } catch {
    // 已提示
  } finally {
    grant.saving = false
  }
}

function resourceName(d: DataResource) {
  return te(d.name) ? t(d.name) : d.code
}

const groupTitle = computed(() => (g: PermGroup) => (te(`permGroup.${g.group}`) ? t(`permGroup.${g.group}`) : g.group))
</script>

<template>
  <div class="ga-page">
    <el-card>
      <div class="ga-toolbar">
        <el-button @click="load">{{ t('common.reset') }}</el-button>
        <div class="ga-toolbar__spacer" />
        <el-button v-perm="'system:role:create'" type="primary" data-test="role-create" @click="openCreate">{{ t('common.create') }}</el-button>
      </div>
    </el-card>
    <el-card>
      <el-table v-loading="loading" :data="roles" stripe data-test="role-table">
        <el-table-column prop="id" :label="t('common.id')" width="70" />
        <el-table-column prop="code" :label="t('sys.role.code')" min-width="120" />
        <el-table-column prop="name" :label="t('sys.role.name')" min-width="140" />
        <el-table-column :label="t('sys.role.super')" width="110">
          <template #default="{ row }">
            <el-tag v-if="(row as Role).isSuper" type="danger" size="small">{{ t('common.yes') }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column :label="t('common.status')" min-width="100">
          <template #default="{ row }">
            <el-tag :type="(row as Role).status === 1 ? 'success' : 'info'" size="small">{{ (row as Role).status === 1 ? t('common.enabled') : t('common.disabled') }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="sort" :label="t('common.sort')" min-width="120" />
        <el-table-column prop="remark" :label="t('common.remark')" min-width="140" show-overflow-tooltip />
        <el-table-column :label="t('common.createdAt')" min-width="170">
          <template #default="{ row }">{{ formatTime((row as Role).createdAt) }}</template>
        </el-table-column>
        <el-table-column :label="t('common.actions')" width="220" fixed="right">
          <template #default="{ row }">
            <!-- 内置超管角色只有超管能改（D-035），这里只是置灰，挡住越权的是后端 -->
            <el-button v-perm="'system:role:update'" link type="primary" :disabled="(row as Role).isSuper && !auth.user?.super" @click="openEdit(row as Role)">{{ t('common.edit') }}</el-button>
            <el-button v-perm="'system:role:grant'" link type="primary" :disabled="(row as Role).isSuper" data-test="role-grant" @click="openGrant(row as Role)">{{ t('sys.role.grant') }}</el-button>
            <el-button v-perm="'system:role:delete'" link type="danger" :disabled="(row as Role).isSuper" @click="remove(row as Role)">{{ t('common.delete') }}</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog v-model="dialog.visible" :title="dialog.id === 0 ? t('sys.role.createTitle') : t('sys.role.editTitle')" width="480px" destroy-on-close>
      <el-form ref="formRef" :model="form" :rules="rules" label-width="auto" @submit.prevent="submit">
        <el-form-item prop="code" :label="t('sys.role.code')">
          <el-input v-model="form.code" :disabled="dialog.id !== 0" maxlength="64" data-test="role-form-code" />
          <div v-if="dialog.id === 0" class="ga-form-hint">{{ t('sys.role.codeRule') }}</div>
        </el-form-item>
        <el-form-item prop="name" :label="t('sys.role.name')">
          <el-input v-model="form.name" maxlength="64" data-test="role-form-name" />
        </el-form-item>
        <el-form-item prop="status" :label="t('common.status')">
          <el-switch v-model="form.status" :active-value="1" :inactive-value="0" />
        </el-form-item>
        <el-form-item prop="sort" :label="t('common.sort')">
          <el-input-number v-model="form.sort" :min="0" />
        </el-form-item>
        <el-form-item prop="remark" :label="t('common.remark')">
          <el-input v-model="form.remark" type="textarea" maxlength="255" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialog.visible = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :loading="dialog.saving" data-test="role-form-submit" @click="submit">{{ t('common.save') }}</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="grant.visible" :title="t('sys.role.grantTitle', { name: grant.role?.name ?? '' })" width="640px" data-test="grant-dialog">
      <el-tabs v-model="grant.tab">
        <el-tab-pane :label="t('sys.role.tabPerms')" name="perms">
          <el-alert v-if="!auth.user?.super" :title="t('sys.role.grantLimit')" type="info" :closable="false" show-icon style="margin-bottom: 12px" />
          <div v-loading="grant.loading" class="perm-groups">
            <div v-for="g in tree" :key="g.group" class="perm-group">
              <el-checkbox
                :model-value="groupState(g).all"
                :indeterminate="groupState(g).some"
                :disabled="groupState(g).enabled.length === 0"
                class="perm-group__title"
                @change="(v: string | number | boolean) => toggleGroup(g, v === true)"
              >
                {{ groupTitle(g) }}
              </el-checkbox>
              <div class="perm-group__items">
                <el-checkbox
                  v-for="p in g.perms"
                  :key="p.code"
                  :model-value="checked.has(p.code)"
                  :disabled="disabled(p)"
                  :data-test="`perm-${p.code}`"
                  @change="(v: string | number | boolean) => toggle(p.code, v === true)"
                >
                  {{ permName(p.code, p.name) }}
                  <el-tag v-if="p.sensitive" type="danger" size="small" effect="plain" style="margin-left: 4px">{{ t('sys.role.sensitive') }}</el-tag>
                  <span class="perm-code">{{ p.code }}</span>
                </el-checkbox>
              </div>
            </div>
          </div>
        </el-tab-pane>
        <el-tab-pane :label="t('sys.role.tabData')" name="data" data-test="grant-tab-data">
          <el-alert :title="t('sys.role.dataHint')" type="info" :closable="false" show-icon style="margin-bottom: 8px" />
          <el-alert v-if="!auth.user?.super" :title="t('sys.role.dataLimit')" type="warning" :closable="false" show-icon style="margin-bottom: 12px" />
          <div v-loading="grant.loading" class="perm-groups">
            <div v-for="d in resources" :key="d.code" class="perm-group" :data-test="`data-${d.code}`">
              <div class="data-res__head">
                <span class="perm-group__title">{{ resourceName(d) }}</span>
                <span class="perm-code">{{ d.code }}</span>
                <span class="data-res__default">{{ t('sys.role.dataDefault', { scope: t(scopeLabel[d.default]) }) }}</span>
              </div>
              <el-radio-group v-model="scopes[d.code]" class="data-res__scopes">
                <el-radio v-for="s in d.scopes" :key="s" :value="s" :data-test="`scope-${d.code}-${s}`">{{ t(scopeLabel[s]) }}</el-radio>
              </el-radio-group>
              <div class="data-res__perms">
                {{ t('sys.role.dataPerms') }}：
                <span v-for="p in d.perms" :key="p" class="perm-code" :class="{ 'data-res__perm--on': checked.has(p) }">{{ p }}</span>
              </div>
            </div>
          </div>
        </el-tab-pane>
      </el-tabs>
      <template #footer>
        <el-button @click="grant.visible = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :loading="grant.saving" :disabled="!grant.loaded || grant.loading" data-test="grant-submit" @click="saveGrant">{{ t('common.save') }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.perm-groups {
  max-height: 60vh;
  overflow: auto;
}

.perm-group {
  border: 1px solid var(--ga-border);
  border-radius: var(--ga-radius);
  padding: 8px 12px;
  margin-bottom: 10px;
}

.perm-group__title {
  font-weight: 600;
}

.perm-group__items {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 20px;
  padding: 6px 0 0 22px;
}

.perm-code {
  margin-left: 6px;
  color: var(--ga-text-secondary);
  font-size: 12px;
}

.data-res__head {
  display: flex;
  align-items: baseline;
  gap: 4px;
  flex-wrap: wrap;
}

.data-res__default {
  margin-left: auto;
  color: var(--ga-text-secondary);
  font-size: 12px;
}

.data-res__scopes {
  padding: 8px 0 4px;
}

.data-res__perms {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 2px 0;
  color: var(--ga-text-secondary);
  font-size: 12px;
}

.data-res__perms .perm-code {
  white-space: nowrap;
}

.data-res__perm--on {
  color: var(--ga-primary);
}
</style>
