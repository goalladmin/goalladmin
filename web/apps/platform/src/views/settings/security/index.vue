<script setup lang="ts">
// 安全设置（D-034）：只读展示本端生效的登录防护和密码策略。
// 策略只能在服务器的配置文件里改、重启生效（D-024），这个页面没有保存入口。
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { CircleCheck, CopyDocument } from '@element-plus/icons-vue'
import { useI18n } from '@ga/shell'

import { securityApi } from '../../../api/system'
import type { PolicyGroup, PolicyItem, PolicySource } from '../../../api/system'

const { t } = useI18n()

const groups: PolicyGroup[] = ['captcha', 'password', 'rate', 'lock', 'expiry']
// 每个页签下始终生效、不能配置的规则
const fixed: Record<PolicyGroup, string[]> = {
  captcha: ['captchaDigits', 'captchaCount'],
  password: ['pwdLetterDigit', 'pwdNotUsername', 'pwdNotPrevious', 'pwdGenerated'],
  rate: ['rateNoOff', 'rateEvent'],
  lock: ['lockNoOff', 'lockEvent'],
  expiry: ['mustChange', 'expiryLogin'],
}

const active = ref<PolicyGroup>('captcha')
const items = ref<PolicyItem[]>([])
const portal = ref('')
const loading = ref(false)

const byGroup = computed(() => {
  const out: Record<string, PolicyItem[]> = {}
  for (const g of groups) out[g] = items.value.filter((it) => it.group === g)
  return out
})

onMounted(async () => {
  loading.value = true
  try {
    const res = await securityApi.policy()
    items.value = res.items
    portal.value = res.portal
  } finally {
    loading.value = false
  }
})

const u = (key: string, n: number) => t(`settings.security.unit.${key}`, { n })

function duration(s: number): string {
  if (s % 3600 === 0) return u('hours', s / 3600)
  if (s % 60 === 0) return u('minutes', s / 60)
  return u('seconds', s)
}

function fmt(it: PolicyItem, v: boolean | number | undefined): string {
  if (it.kind === 'bool') return v ? t('settings.security.on') : t('settings.security.off')
  const n = Number(v ?? 0)
  switch (it.kind) {
    case 'times':
      return u('times', n)
    case 'perMinute':
      return u('perMinute', n)
    case 'chars':
      return u('chars', n)
    case 'days':
      return n === 0 ? t('settings.security.unit.never') : u('days', n)
    case 'seconds':
      return duration(n)
    default:
      return String(n)
  }
}

function range(it: PolicyItem): string {
  if (it.kind === 'bool') return t('settings.security.onlyOn')
  const span = `${fmt(it, it.min)} – ${fmt(it, it.max)}`
  return it.kind === 'days' ? t('settings.security.rangeDays', { span }) : span
}

/** 当前值在配置文件里的写法：时长写成 15m、1h，开关写 true / false。 */
function syntax(it: PolicyItem): string {
  if (it.kind === 'bool') return String(Boolean(it.value))
  const n = Number(it.value)
  if (it.kind !== 'seconds') return String(n)
  if (n % 3600 === 0) return `${n / 3600}h`
  if (n % 60 === 0) return `${n / 60}m`
  return `${n}s`
}

const sourceType: Record<PolicySource, 'primary' | 'warning' | 'info'> = { config: 'primary', code: 'warning', default: 'info' }

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
    <el-alert :title="t('settings.security.hint')" type="info" :closable="false" show-icon data-test="security-hint">
      <template #default>{{ t('settings.security.howto', { portal }) }}</template>
    </el-alert>
    <el-card v-loading="loading">
      <el-tabs v-model="active" data-test="security-tabs">
        <el-tab-pane v-for="g in groups" :key="g" :name="g" :label="t(`settings.security.tabs.${g}`)">
          <el-table :data="byGroup[g]" :data-test="`security-table-${g}`" row-key="key">
            <el-table-column :label="t('settings.security.item')" min-width="240">
              <template #default="{ row }">
                <div class="ga-sec__name">{{ t(`settings.security.items.${(row as PolicyItem).key}`) }}</div>
                <div class="ga-sec__hint">{{ t(`settings.security.hints.${(row as PolicyItem).key}`) }}</div>
              </template>
            </el-table-column>
            <el-table-column :label="t('settings.security.value')" min-width="110">
              <template #default="{ row }">
                <el-tag
                  v-if="(row as PolicyItem).kind === 'bool'"
                  :type="(row as PolicyItem).value ? 'success' : 'info'"
                  effect="plain"
                  :data-test="`policy-${(row as PolicyItem).key}`"
                >
                  {{ fmt(row as PolicyItem, (row as PolicyItem).value) }}
                </el-tag>
                <b v-else :data-test="`policy-${(row as PolicyItem).key}`">{{ fmt(row as PolicyItem, (row as PolicyItem).value) }}</b>
              </template>
            </el-table-column>
            <el-table-column :label="t('settings.security.range')" min-width="200">
              <template #default="{ row }">{{ range(row as PolicyItem) }}</template>
            </el-table-column>
            <el-table-column :label="t('settings.security.default')" min-width="100">
              <template #default="{ row }">{{ fmt(row as PolicyItem, (row as PolicyItem).default) }}</template>
            </el-table-column>
            <el-table-column :label="t('settings.security.source')" min-width="100">
              <template #default="{ row }">
                <el-tag size="small" :type="sourceType[(row as PolicyItem).source]">{{ t(`settings.security.sources.${(row as PolicyItem).source}`) }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column :label="t('settings.security.configKey')" min-width="360">
              <template #default="{ row }">
                <span class="ga-mono ga-sec__key">{{ (row as PolicyItem).configKey }}</span>
                <el-button link type="primary" :icon="CopyDocument" :title="t('common.copy')" @click="copy((row as PolicyItem).configKey)" />
                <div class="ga-sec__hint" :data-test="`policy-syntax-${(row as PolicyItem).key}`">
                  {{ t('settings.security.syntax') }} <span class="ga-mono ga-sec__syntax">{{ (row as PolicyItem).key }}: {{ syntax(row as PolicyItem) }}</span>
                </div>
              </template>
            </el-table-column>
          </el-table>
          <div class="ga-sec__fixed" :data-test="`security-fixed-${g}`">
            <div class="ga-sec__fixed-title">{{ t('settings.security.fixedTitle') }}</div>
            <div v-for="k in fixed[g]" :key="k" class="ga-sec__fixed-row">
              <el-icon class="ga-sec__fixed-icon"><CircleCheck /></el-icon>
              <span>{{ t(`settings.security.fixed.${k}`) }}</span>
            </div>
          </div>
        </el-tab-pane>
      </el-tabs>
    </el-card>
  </div>
</template>

<style scoped>
.ga-sec__name {
  font-weight: 600;
}
.ga-sec__hint {
  margin-top: 2px;
  color: var(--el-text-color-secondary);
  font-size: 12px;
  line-height: 1.5;
}
.ga-sec__key {
  margin-right: 4px;
  font-size: 12px;
  white-space: nowrap;
}
.ga-sec__syntax {
  margin-left: 4px;
  color: var(--el-text-color-regular);
  white-space: nowrap;
}
.ga-sec__fixed {
  margin-top: 16px;
  padding: 12px 16px;
  border-radius: 6px;
  background: var(--el-fill-color-light);
}
.ga-sec__fixed-title {
  margin-bottom: 8px;
  font-weight: 600;
}
.ga-sec__fixed-row {
  display: flex;
  gap: 8px;
  align-items: flex-start;
  margin-top: 4px;
  color: var(--el-text-color-regular);
  font-size: 13px;
  line-height: 1.6;
}
.ga-sec__fixed-icon {
  flex: none;
  margin-top: 3px;
  color: var(--el-color-success);
}
</style>
