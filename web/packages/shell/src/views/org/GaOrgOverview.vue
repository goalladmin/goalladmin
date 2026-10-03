<script setup lang="ts">
// 主体端概览：本主体资料及有查看权限的计数；IP 名单在独立安全设置页面（D-090）。
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { formatTime } from '../../format'
import { orgApi } from '../../org/api'
import type { OrgOverview } from '../../org/api'

const { t, te } = useI18n()

const data = ref<OrgOverview | null>(null)
const loading = ref(false)
async function load() {
  loading.value = true
  try {
    data.value = await orgApi.overview()
  } catch {
    // 已提示
  } finally {
    loading.value = false
  }
}
void load()

// 计数的顺序：先内置的几项，再按键名
const countOrder = ['accounts', 'sessions', 'roles']
const counts = computed(() => {
  const c = data.value?.counts ?? {}
  return Object.keys(c)
    .sort((a, b) => (countOrder.indexOf(a) + 1 || 99) - (countOrder.indexOf(b) + 1 || 99) || a.localeCompare(b))
    .map((k) => ({ key: k, value: c[k] ?? 0, label: te(`org.overview.counts.${k}`) ? t(`org.overview.counts.${k}`) : k }))
})

</script>

<template>
  <div v-loading="loading" class="ga-page" data-test="org-overview">
    <el-card v-if="data">
      <template #header>
        <div class="ga-org-overview__head">
          <span>{{ t('org.overview.info') }}</span>
          <el-tag v-if="data.owner" type="warning" size="small" data-test="org-owner-tag">{{ t('org.overview.owner') }}</el-tag>
        </div>
      </template>
      <el-descriptions :column="2" border>
        <el-descriptions-item :label="t('org.overview.code')"><span class="ga-mono" data-test="org-code">{{ data.org.code }}</span></el-descriptions-item>
        <el-descriptions-item :label="t('org.overview.name')">{{ data.org.name }}</el-descriptions-item>
        <el-descriptions-item :label="t('org.overview.contactName')">{{ data.org.contactName || '-' }}</el-descriptions-item>
        <el-descriptions-item :label="t('org.overview.contactPhone')">{{ data.org.contactPhone || '-' }}</el-descriptions-item>
        <el-descriptions-item :label="t('common.status')">
          <el-tag :type="data.org.status === 1 ? 'success' : 'info'" size="small">{{ data.org.status === 1 ? t('common.enabled') : t('common.disabled') }}</el-tag>
        </el-descriptions-item>
        <el-descriptions-item :label="t('org.overview.createdAt')">{{ formatTime(data.org.createdAt) }}</el-descriptions-item>
      </el-descriptions>
    </el-card>

    <div v-if="counts.length" class="ga-org-overview__counts">
      <el-card v-for="c in counts" :key="c.key" shadow="hover" :data-test="`org-count-${c.key}`">
        <div class="ga-org-overview__num">{{ c.value }}</div>
        <div class="ga-org-overview__label">{{ c.label }}</div>
      </el-card>
    </div>
  </div>
</template>

<style scoped>
.ga-org-overview__head {
  display: flex;
  align-items: center;
  gap: 8px;
}

.ga-org-overview__counts {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
  gap: 16px;
}

.ga-org-overview__num {
  font-size: 28px;
  font-weight: 600;
  color: var(--ga-text);
}

.ga-org-overview__label {
  margin-top: 4px;
  color: var(--ga-text-secondary);
}

</style>
