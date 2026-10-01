<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import type { FormInstance, FormRules } from 'element-plus'
import { GaAvatar, formatTime, useAuthStore, useI18n, useTable } from '@ga/shell'

import { buildTree, deptApi, postApi, roleApi, userApi } from '../../../api/system'
import type { CreateUserInput, DeptOption, PostOption, Role, TreeNode, TreeOption, UpdateUserInput, UserView } from '../../../api/system'

const { t } = useI18n()
const auth = useAuthStore()

// ---- 列表 ----
const table = useTable<{ keyword: string; status: number | undefined; deptId: number | undefined }, UserView>({
  query: { keyword: '', status: undefined, deptId: undefined },
  fetch: (p) => userApi.list(p),
})

// ---- 部门、岗位（D-033） ----
const depts = ref<DeptOption[]>([])
const posts = ref<PostOption[]>([])
async function loadOrg() {
  try {
    ;[depts.value, posts.value] = await Promise.all([deptApi.options(), postApi.options()])
  } catch {
    // 已提示；没有也不影响其他字段
  }
}
void loadOrg()

/** 部门树选项。keep 是当前用户原来的部门：停用的部门不能新选，但原来就在里面的可以保留。 */
function deptTree(keep = 0): TreeOption[] {
  const mark = (nodes: TreeNode<DeptOption>[]): TreeOption[] =>
    nodes.map((n) => ({ value: n.id, label: n.name, disabled: n.status !== 1 && n.id !== keep, children: n.children ? mark(n.children) : undefined }))
  return mark(buildTree(depts.value))
}
const filterTree = computed<TreeOption[]>(() => {
  const mark = (nodes: TreeNode<DeptOption>[]): TreeOption[] => nodes.map((n) => ({ value: n.id, label: n.name, children: n.children ? mark(n.children) : undefined }))
  return mark(buildTree(depts.value))
})

const roles = ref<Role[]>([])
async function loadRoles() {
  roles.value = await roleApi.list()
}
void loadRoles()
const assignableRoles = computed(() => roles.value.filter((r) => r.status === 1 && (auth.user?.super || !r.isSuper)))

// ---- 新建 / 编辑 ----
type Mode = 'create' | 'edit'
const dialog = reactive({ visible: false, mode: 'create' as Mode, id: 0, saving: false })
const form = reactive<CreateUserInput & UpdateUserInput>({
  username: '',
  password: '',
  displayName: '',
  email: '',
  phone: '',
  roleIds: [],
  deptId: undefined,
  postIds: [],
  sort: 0,
  remark: '',
  bio: '',
})
// 编辑时用户原来的部门、岗位：停用了也允许保留
const original = reactive({ deptId: 0, postIds: [] as number[] })
const formDeptTree = computed(() => deptTree(original.deptId))
const selectablePosts = computed(() => posts.value.map((p) => ({ ...p, disabled: p.status !== 1 && !original.postIds.includes(p.id) })))
const formRef = ref<FormInstance>()
const rules = computed<FormRules>(() => ({
  username: [{ required: dialog.mode === 'create', message: () => t('common.required'), trigger: 'blur' }],
  displayName: [{ required: dialog.mode === 'edit', message: () => t('common.required'), trigger: 'blur' }],
  email: [{ type: 'email', message: () => t('sys.user.emailInvalid'), trigger: 'blur' }],
}))

function openCreate() {
  dialog.mode = 'create'
  dialog.id = 0
  Object.assign(form, { username: '', password: '', displayName: '', email: '', phone: '', roleIds: [], deptId: undefined, postIds: [], sort: 0, remark: '', bio: '' })
  Object.assign(original, { deptId: 0, postIds: [] })
  dialog.visible = true
}

function openEdit(u: UserView) {
  dialog.mode = 'edit'
  dialog.id = u.id
  const postIds = u.posts.map((p) => p.id)
  Object.assign(form, { username: u.username, password: '', displayName: u.displayName, email: u.email, phone: u.phone, roleIds: [], deptId: u.deptId || undefined, postIds, sort: u.sort, remark: u.remark, bio: u.bio })
  Object.assign(original, { deptId: u.deptId, postIds })
  dialog.visible = true
}

// 只显示一次的初始密码
const reveal = reactive({ visible: false, username: '', password: '' })
function showPassword(username: string, password: string) {
  reveal.username = username
  reveal.password = password
  reveal.visible = true
}
async function copyPassword() {
  try {
    await navigator.clipboard.writeText(reveal.password)
    ElMessage.success(t('common.copied'))
  } catch {
    // 非安全上下文没有剪贴板，用户手动复制
  }
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
    if (dialog.mode === 'create') {
      const res = await userApi.create({
        username: form.username.trim(),
        password: form.password || undefined,
        displayName: form.displayName || undefined,
        email: form.email || undefined,
        phone: form.phone || undefined,
        roleIds: form.roleIds,
        deptId: form.deptId || 0,
        postIds: form.postIds,
        sort: form.sort,
        remark: form.remark,
      })
      dialog.visible = false
      ElMessage.success(t('common.success'))
      if (res.initialPassword) showPassword(res.user.username, res.initialPassword)
    } else {
      await userApi.update(dialog.id, {
        displayName: form.displayName,
        email: form.email,
        phone: form.phone,
        deptId: form.deptId || 0,
        postIds: form.postIds ?? [],
        sort: form.sort,
        remark: form.remark,
        bio: form.bio,
      })
      dialog.visible = false
      ElMessage.success(t('common.success'))
    }
    await table.reload()
  } catch {
    // 统一提示已经弹过
  } finally {
    dialog.saving = false
  }
}

// ---- 超管账号保护（D-035）：非超管不能改超管账号；重置密码只有超管能做，超管的密码只能用命令行 ----
// 这里只是把按钮置灰并说明原因，真正挡住越权的是后端。
const isSuperRow = (u: UserView) => u.roles.some((r) => r.isSuper)
const lockedForMe = (u: UserView) => !auth.user?.super && isSuperRow(u)

// ---- 状态 / 重置密码 / 分配角色 ----
async function toggleStatus(u: UserView) {
  const next = u.status === 1 ? 0 : 1
  try {
    await ElMessageBox.confirm(t(next === 1 ? 'sys.user.confirmEnable' : 'sys.user.confirmDisable', { name: u.username }), t('common.confirmTitle'), { type: 'warning' })
  } catch {
    return
  }
  await userApi.setStatus(u.id, next)
  ElMessage.success(t('common.success'))
  await table.reload()
}

// 管理员只能清除别人的头像，不能替别人换（D-040）
async function clearAvatar(u: UserView) {
  try {
    await ElMessageBox.confirm(t('sys.user.confirmClearAvatar', { name: u.username }), t('common.confirmTitle'), { type: 'warning' })
  } catch {
    return
  }
  await userApi.clearAvatar(u.id)
  ElMessage.success(t('common.success'))
  await table.reload()
}

async function resetPassword(u: UserView) {
  try {
    await ElMessageBox.confirm(t('sys.user.confirmReset', { name: u.username }), t('common.confirmTitle'), { type: 'warning' })
  } catch {
    return
  }
  const res = await userApi.resetPassword(u.id)
  showPassword(u.username, res.initialPassword)
  await table.reload()
}

const roleDialog = reactive({ visible: false, id: 0, username: '', roleIds: [] as number[], saving: false })
function openRoles(u: UserView) {
  roleDialog.id = u.id
  roleDialog.username = u.username
  roleDialog.roleIds = u.roles.map((r) => r.id)
  roleDialog.visible = true
}
async function saveRoles() {
  roleDialog.saving = true
  try {
    await userApi.assignRoles(roleDialog.id, roleDialog.roleIds)
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
          <el-input v-model="table.query.keyword" clearable :placeholder="t('sys.user.keywordPlaceholder')" style="width: 220px" @keyup.enter="table.search()" />
        </el-form-item>
        <el-form-item :label="t('sys.user.dept')">
          <el-tree-select
            v-model="table.query.deptId"
            :data="filterTree"
            check-strictly
            clearable
            filterable
            default-expand-all
            :render-after-expand="false"
            :placeholder="t('common.all')"
            style="width: 200px"
            data-test="user-filter-dept"
          />
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
        <el-button v-perm="'system:user:create'" type="primary" data-test="user-create" @click="openCreate">{{ t('common.create') }}</el-button>
      </el-form>
    </el-card>

    <el-card>
      <el-table v-loading="table.loading" :data="table.list" stripe data-test="user-table">
        <el-table-column prop="id" :label="t('common.id')" width="70" />
        <el-table-column :label="t('common.username')" min-width="210">
          <template #default="{ row }">
            <span class="ga-user__who">
              <GaAvatar :size="26" :value="(row as UserView).avatar" :name="(row as UserView).username" />
              <span>{{ (row as UserView).username }}</span>
            </span>
          </template>
        </el-table-column>
        <el-table-column prop="displayName" :label="t('common.displayName')" min-width="120" />
        <el-table-column :label="t('sys.user.dept')" min-width="120">
          <template #default="{ row }">{{ (row as UserView).deptName || '-' }}</template>
        </el-table-column>
        <el-table-column :label="t('sys.user.posts')" min-width="150">
          <template #default="{ row }">
            <el-tag v-for="p in (row as UserView).posts" :key="p.id" size="small" type="info" class="ga-tag">{{ p.name }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column :label="t('sys.user.roles')" min-width="160">
          <template #default="{ row }">
            <el-tag v-for="r in (row as UserView).roles" :key="r.id" size="small" :type="r.isSuper ? 'danger' : 'primary'" class="ga-tag">{{ r.name }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column :label="t('common.status')" min-width="100">
          <template #default="{ row }">
            <el-tag :type="(row as UserView).status === 1 ? 'success' : 'info'" size="small">{{ (row as UserView).status === 1 ? t('common.enabled') : t('common.disabled') }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column :label="t('sys.user.lastLogin')" min-width="170">
          <template #default="{ row }">{{ formatTime((row as UserView).lastLoginAt) }}</template>
        </el-table-column>
        <el-table-column :label="t('common.createdAt')" min-width="170">
          <template #default="{ row }">{{ formatTime((row as UserView).createdAt) }}</template>
        </el-table-column>
        <el-table-column :label="t('common.actions')" width="360" fixed="right">
          <template #default="{ row }">
            <el-tooltip :disabled="!lockedForMe(row as UserView)" :content="t('sys.user.superProtectedHint')" placement="top">
              <span class="ga-user__actions" :data-test="`user-actions-${(row as UserView).username}`">
                <el-button v-perm="'system:user:update'" link type="primary" :disabled="lockedForMe(row as UserView)" @click="openEdit(row as UserView)">{{ t('common.edit') }}</el-button>
                <el-button v-perm="'system:user:assign-role'" link type="primary" :disabled="lockedForMe(row as UserView)" @click="openRoles(row as UserView)">{{ t('sys.user.assignRoles') }}</el-button>
                <el-button
                  v-if="auth.user?.super"
                  link
                  type="warning"
                  :disabled="isSuperRow(row as UserView)"
                  :title="isSuperRow(row as UserView) ? t('sys.user.resetSuperHint') : ''"
                  data-test="user-reset-password"
                  @click="resetPassword(row as UserView)"
                >
                  {{ t('sys.user.resetPassword') }}
                </el-button>
                <el-button
                  v-if="(row as UserView).avatar"
                  v-perm="'system:user:update'"
                  link
                  type="warning"
                  :disabled="lockedForMe(row as UserView)"
                  data-test="user-clear-avatar"
                  @click="clearAvatar(row as UserView)"
                >
                  {{ t('sys.user.clearAvatar') }}
                </el-button>
                <el-button
                  v-perm="'system:user:status'"
                  link
                  :type="(row as UserView).status === 1 ? 'danger' : 'success'"
                  :disabled="(row as UserView).id === auth.user?.id || lockedForMe(row as UserView)"
                  @click="toggleStatus(row as UserView)"
                >
                  {{ (row as UserView).status === 1 ? t('common.disable') : t('common.enable') }}
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

    <el-dialog v-model="dialog.visible" :title="dialog.mode === 'create' ? t('sys.user.createTitle') : t('sys.user.editTitle')" width="520px" destroy-on-close>
      <el-form ref="formRef" :model="form" :rules="rules" label-width="auto" @submit.prevent="submit">
        <el-form-item prop="username" :label="t('common.username')">
          <el-input v-model="form.username" :disabled="dialog.mode === 'edit'" maxlength="64" data-test="user-form-username" />
        </el-form-item>
        <el-form-item v-if="dialog.mode === 'create'" prop="password" :label="t('sys.user.password')">
          <el-input v-model="form.password" type="password" show-password maxlength="72" autocomplete="new-password" data-test="user-form-password" />
          <div class="ga-form-hint">{{ t('sys.user.passwordHint') }}</div>
        </el-form-item>
        <el-form-item prop="displayName" :label="t('common.displayName')">
          <el-input v-model="form.displayName" maxlength="64" />
        </el-form-item>
        <el-form-item prop="email" :label="t('sys.user.email')">
          <el-input v-model="form.email" maxlength="128" />
        </el-form-item>
        <el-form-item prop="phone" :label="t('sys.user.phone')">
          <el-input v-model="form.phone" maxlength="32" />
        </el-form-item>
        <el-form-item prop="deptId" :label="t('sys.user.dept')">
          <el-tree-select
            v-model="form.deptId"
            :data="formDeptTree"
            check-strictly
            clearable
            filterable
            default-expand-all
            :render-after-expand="false"
            :placeholder="t('sys.user.noDept')"
            style="width: 100%"
            data-test="user-form-dept"
          />
        </el-form-item>
        <el-form-item prop="postIds" :label="t('sys.user.posts')">
          <el-select v-model="form.postIds" multiple filterable style="width: 100%" data-test="user-form-posts">
            <el-option v-for="p in selectablePosts" :key="p.id" :value="p.id" :label="p.name" :disabled="p.disabled" />
          </el-select>
        </el-form-item>
        <el-form-item v-if="dialog.mode === 'create'" prop="roleIds" :label="t('sys.user.roles')">
          <el-select v-model="form.roleIds" multiple style="width: 100%" data-test="user-form-roles">
            <el-option v-for="r in assignableRoles" :key="r.id" :value="r.id" :label="r.name" />
          </el-select>
        </el-form-item>
        <el-form-item prop="sort" :label="t('common.sort')">
          <el-input-number v-model="form.sort" :min="0" />
        </el-form-item>
        <el-form-item prop="remark" :label="t('common.remark')">
          <el-input v-model="form.remark" type="textarea" maxlength="255" />
        </el-form-item>
        <!-- 个人简介由本人在个人中心维护（D-038）；管理员这里能看、能改，编辑时原样带上，不会被清空 -->
        <el-form-item v-if="dialog.mode === 'edit'" prop="bio" :label="t('profile.basic.bio')">
          <el-input v-model="form.bio" type="textarea" maxlength="255" show-word-limit />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialog.visible = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :loading="dialog.saving" data-test="user-form-submit" @click="submit">{{ t('common.save') }}</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="roleDialog.visible" :title="t('sys.user.assignRolesTitle', { name: roleDialog.username })" width="420px">
      <el-select v-model="roleDialog.roleIds" multiple style="width: 100%">
        <el-option v-for="r in assignableRoles" :key="r.id" :value="r.id" :label="r.name" />
      </el-select>
      <template #footer>
        <el-button @click="roleDialog.visible = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :loading="roleDialog.saving" @click="saveRoles">{{ t('common.save') }}</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="reveal.visible" :title="t('sys.user.initialPasswordTitle')" width="420px" data-test="password-reveal">
      <el-alert :title="t('sys.user.initialPasswordHint')" type="warning" :closable="false" show-icon />
      <el-descriptions :column="1" border style="margin-top: 12px">
        <el-descriptions-item :label="t('common.username')">{{ reveal.username }}</el-descriptions-item>
        <el-descriptions-item :label="t('sys.user.password')">
          <span class="ga-mono" data-test="initial-password">{{ reveal.password }}</span>
          <el-button link type="primary" style="margin-left: 8px" @click="copyPassword">{{ t('common.copy') }}</el-button>
        </el-descriptions-item>
      </el-descriptions>
      <template #footer>
        <el-button type="primary" @click="reveal.visible = false">{{ t('common.close') }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.ga-user__who {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}
</style>
