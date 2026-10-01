<script setup lang="ts">
// 部门管理（D-033）：部门树只是组织资料，不影响任何权限判断。
import { computed, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import type { FormInstance, FormRules } from 'element-plus'
import { useI18n } from '@ga/shell'

import { buildTree, deptApi, userApi } from '../../../api/system'
import type { Dept, DeptInput, TreeNode, TreeOption } from '../../../api/system'

const { t } = useI18n()

// ---- 列表（树） ----
const rows = ref<Dept[]>([])
const loading = ref(false)
const keyword = ref('')
async function load() {
  loading.value = true
  try {
    rows.value = await deptApi.list()
  } finally {
    loading.value = false
  }
}
void load()

function resetSearch() {
  keyword.value = ''
  void load()
}

/** 按名称筛选：命中的部门连同它的上级一起显示，树的形状不变。 */
const tree = computed(() => {
  const k = keyword.value.trim().toLowerCase()
  if (!k) return buildTree(rows.value)
  const byId = new Map(rows.value.map((d) => [d.id, d]))
  const keep = new Set<number>()
  for (const d of rows.value) {
    if (!d.name.toLowerCase().includes(k)) continue
    for (let cur: Dept | undefined = d; cur && !keep.has(cur.id); cur = byId.get(cur.parentId)) keep.add(cur.id)
  }
  return buildTree(rows.value.filter((d) => keep.has(d.id)))
})

// ---- 新建 / 编辑 ----
const users = ref<{ id: number; displayName: string }[]>([])
// keepHiddenLeader：部门的负责人不在你的查看范围内（D-055）。没有另选时保存不带负责人，服务端保持原来的
const dialog = reactive({ visible: false, id: 0, saving: false, keepHiddenLeader: false })
const form = reactive<DeptInput>({ parentId: 0, name: '', leaderUserId: undefined, phone: '', email: '', status: 1, sort: 0, remark: '' })
const formRef = ref<FormInstance>()
const rules: FormRules = {
  name: [{ required: true, whitespace: true, message: () => t('common.required'), trigger: 'blur' }],
  email: [{ type: 'email', message: () => t('sys.user.emailInvalid'), trigger: 'blur' }],
}

/** 上级的可选范围：编辑时不能选自己和自己的下级（后端同样会拒绝）。 */
const parentOptions = computed<TreeOption[]>(() => {
  const banned = new Set<number>()
  if (dialog.id) {
    banned.add(dialog.id)
    let grew = true
    while (grew) {
      grew = false
      for (const d of rows.value) {
        if (!banned.has(d.id) && banned.has(d.parentId)) {
          banned.add(d.id)
          grew = true
        }
      }
    }
  }
  const mark = (nodes: TreeNode<Dept>[]): TreeOption[] =>
    nodes.map((n) => ({ value: n.id, label: n.name, disabled: banned.has(n.id), children: n.children ? mark(n.children) : undefined }))
  return [{ value: 0, label: t('sys.dept.top'), children: mark(buildTree(rows.value)) }]
})

async function loadUsers() {
  if (users.value.length) return
  try {
    users.value = await userApi.options()
  } catch {
    // 没有也能保存，只是选不了负责人
  }
}

function openCreate(parent?: Dept) {
  dialog.id = 0
  dialog.keepHiddenLeader = false
  Object.assign(form, { parentId: parent?.id ?? 0, name: '', leaderUserId: undefined, phone: '', email: '', status: 1, sort: 0, remark: '' })
  dialog.visible = true
  void loadUsers()
}

function openEdit(d: Dept) {
  dialog.id = d.id
  dialog.keepHiddenLeader = d.leaderHidden
  Object.assign(form, { parentId: d.parentId, name: d.name, leaderUserId: d.leaderUserId || undefined, phone: d.phone, email: d.email, status: d.status, sort: d.sort, remark: d.remark })
  dialog.visible = true
  void loadUsers()
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
    const input: DeptInput = { ...form, name: form.name.trim(), leaderUserId: form.leaderUserId || 0 }
    if (dialog.keepHiddenLeader && !form.leaderUserId) delete input.leaderUserId
    if (dialog.id === 0) await deptApi.create(input)
    else await deptApi.update(dialog.id, input)
    dialog.visible = false
    ElMessage.success(t('common.success'))
    await load()
  } catch {
    // 已提示
  } finally {
    dialog.saving = false
  }
}

async function remove(d: Dept) {
  try {
    await ElMessageBox.confirm(t('sys.dept.confirmDelete', { name: d.name }), t('common.confirmTitle'), { type: 'warning' })
  } catch {
    return
  }
  try {
    await deptApi.remove(d.id)
    ElMessage.success(t('common.success'))
    await load()
  } catch {
    // 已提示（有下级或还有用户）
  }
}
</script>

<template>
  <div class="ga-page">
    <el-card>
      <div class="ga-toolbar">
        <el-input v-model="keyword" clearable :placeholder="t('sys.dept.searchPlaceholder')" style="width: 220px" data-test="dept-search" />
        <el-button @click="resetSearch">{{ t('common.reset') }}</el-button>
        <div class="ga-toolbar__spacer" />
        <el-button v-perm="'system:dept:create'" type="primary" data-test="dept-create" @click="openCreate()">{{ t('common.create') }}</el-button>
      </div>
    </el-card>
    <el-card>
      <el-table v-loading="loading" :data="tree" row-key="id" default-expand-all data-test="dept-table">
        <el-table-column prop="name" :label="t('sys.dept.name')" min-width="220" />
        <el-table-column :label="t('sys.dept.leader')" min-width="120">
          <template #default="{ row }">{{ (row as Dept).leaderHidden ? t('sys.dept.leaderHidden') : (row as Dept).leaderName || '-' }}</template>
        </el-table-column>
        <el-table-column prop="userCount" :label="t('sys.dept.users')" width="100" align="right" />
        <el-table-column prop="phone" :label="t('sys.user.phone')" min-width="130" />
        <el-table-column prop="email" :label="t('sys.user.email')" min-width="180" show-overflow-tooltip />
        <el-table-column prop="sort" :label="t('common.sort')" width="90" />
        <el-table-column :label="t('common.status')" width="100">
          <template #default="{ row }">
            <el-tag :type="(row as Dept).status === 1 ? 'success' : 'info'" size="small">{{ (row as Dept).status === 1 ? t('common.enabled') : t('common.disabled') }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column :label="t('common.actions')" width="230" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'system:dept:create'" link type="primary" @click="openCreate(row as Dept)">{{ t('sys.dept.addChild') }}</el-button>
            <el-button v-perm="'system:dept:update'" link type="primary" @click="openEdit(row as Dept)">{{ t('common.edit') }}</el-button>
            <el-button v-perm="'system:dept:delete'" link type="danger" @click="remove(row as Dept)">{{ t('common.delete') }}</el-button>
          </template>
        </el-table-column>
      </el-table>
      <el-empty v-if="!loading && !rows.length" :description="t('sys.dept.empty')" />
    </el-card>

    <el-dialog v-model="dialog.visible" :title="dialog.id === 0 ? t('sys.dept.createTitle') : t('sys.dept.editTitle')" width="520px" destroy-on-close>
      <el-form ref="formRef" :model="form" :rules="rules" label-width="auto" @submit.prevent="submit">
        <el-form-item prop="parentId" :label="t('sys.dept.parent')">
          <el-tree-select v-model="form.parentId" :data="parentOptions" check-strictly default-expand-all :render-after-expand="false" style="width: 100%" data-test="dept-form-parent" />
        </el-form-item>
        <el-form-item prop="name" :label="t('sys.dept.name')">
          <el-input v-model="form.name" maxlength="64" data-test="dept-form-name" />
        </el-form-item>
        <el-form-item prop="leaderUserId" :label="t('sys.dept.leader')">
          <el-select v-model="form.leaderUserId" filterable clearable :placeholder="dialog.keepHiddenLeader ? t('sys.dept.leaderHiddenKeep') : t('sys.dept.noLeader')" style="width: 100%">
            <el-option v-for="u in users" :key="u.id" :value="u.id" :label="u.displayName" />
          </el-select>
          <el-button v-if="dialog.keepHiddenLeader && !form.leaderUserId" link type="danger" @click="dialog.keepHiddenLeader = false">{{ t('sys.dept.clearLeader') }}</el-button>
        </el-form-item>
        <el-form-item prop="phone" :label="t('sys.user.phone')">
          <el-input v-model="form.phone" maxlength="32" />
        </el-form-item>
        <el-form-item prop="email" :label="t('sys.user.email')">
          <el-input v-model="form.email" maxlength="128" />
        </el-form-item>
        <el-form-item prop="sort" :label="t('common.sort')">
          <el-input-number v-model="form.sort" :min="0" :max="1000000" />
        </el-form-item>
        <el-form-item prop="status" :label="t('common.status')">
          <el-switch v-model="form.status" :active-value="1" :inactive-value="0" />
          <div class="ga-form-hint">{{ t('sys.dept.statusHint') }}</div>
        </el-form-item>
        <el-form-item prop="remark" :label="t('common.remark')">
          <el-input v-model="form.remark" type="textarea" maxlength="255" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialog.visible = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :loading="dialog.saving" data-test="dept-form-submit" @click="submit">{{ t('common.save') }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>
