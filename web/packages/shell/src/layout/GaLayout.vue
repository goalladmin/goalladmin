<script setup lang="ts">
import { defineComponent, h, onBeforeUnmount, onMounted, watch } from 'vue'
import type { Component } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'

import { Close } from '@element-plus/icons-vue'

import GaSidebar from './GaSidebar.vue'
import GaTopbar from './GaTopbar.vue'
import GaTabs from './GaTabs.vue'
import { useLayoutStore } from './store'
import { useAuthStore } from '../auth/store'
import { firstMenuPath, menuTitle } from '../router/menu'

const route = useRoute()
const router = useRouter()
const layout = useLayoutStore()
const auth = useAuthStore()
const { t, locale } = useI18n()

// 响应式断点
const mq = typeof window !== 'undefined' ? window.matchMedia('(max-width: 768px)') : null
const onMq = () => layout.setMobile(!!mq?.matches)
onMounted(() => {
  onMq()
  mq?.addEventListener('change', onMq)
})
onBeforeUnmount(() => mq?.removeEventListener('change', onMq))

// 标签页：菜单路由和个人信息页进标签栏
watch(
  () => route.fullPath,
  () => {
    if (!route.name || !route.meta.titleKey) return
    if (!route.meta.menu && route.path !== '/profile') return
    layout.openTab({
      name: String(route.name),
      path: route.path,
      fullPath: route.fullPath,
      titleKey: route.meta.titleKey,
      titles: route.meta.titles,
      icon: route.meta.icon,
      keepAlive: route.meta.keepAlive === true,
    })
    document.title = `${menuTitle(t, locale.value, route.meta.titleKey, route.meta.titles)} · ${t('shell.appName')}`
  },
  { immediate: true },
)

// 首页（第一个可见的菜单页）固定在标签栏最前面，不能关闭（D-027）
watch(
  () => auth.menus,
  (menus) => {
    const home = firstMenuPath(menus)
    if (!home) return
    const r = router.resolve(home)
    if (!r.name || !r.meta.menu || !r.meta.titleKey) return
    layout.openTab({
      name: String(r.name),
      path: r.path,
      fullPath: r.fullPath,
      titleKey: r.meta.titleKey,
      titles: r.meta.titles,
      icon: r.meta.icon,
      keepAlive: r.meta.keepAlive === true,
      affix: true,
    })
  },
  { immediate: true },
)

// Esc 退出内容区最大化
function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape' && layout.maximized) layout.maximized = false
}
onMounted(() => window.addEventListener('keydown', onKey))
onBeforeUnmount(() => window.removeEventListener('keydown', onKey))

// KeepAlive 按组件名缓存，而 <script setup> 页面的名字都来自文件名（大多是 index），
// 所以用路由名包一层，给每个页面一个唯一的组件名。
const wrapped = new Map<string, Component>()
function wrap(comp: Component, name: string): Component {
  let w = wrapped.get(name)
  if (!w) {
    w = defineComponent({ name, setup: () => () => h('div', { class: 'ga-page-host' }, [h(comp)]) })
    wrapped.set(name, w)
  }
  return w
}

function toggle() {
  if (layout.isMobile) layout.drawerOpen = !layout.drawerOpen
  else layout.toggleCollapsed()
}
</script>

<template>
  <div class="ga-layout" :class="{ 'is-collapsed': layout.collapsed, 'is-mobile': layout.isMobile, 'is-maximized': layout.maximized, 'is-compact': layout.prefs.compact }">
    <el-drawer v-if="layout.isMobile" v-model="layout.drawerOpen" direction="ltr" :with-header="false" size="220px" class="ga-layout__drawer">
      <GaSidebar :collapsed="false" @navigate="layout.drawerOpen = false" />
    </el-drawer>
    <aside v-else-if="!layout.maximized" class="ga-layout__aside" :class="`ga-layout__aside--${layout.prefs.sidebarTheme}`">
      <GaSidebar :collapsed="layout.collapsed" />
    </aside>
    <div class="ga-layout__body">
      <GaTopbar v-if="!layout.maximized" @toggle="toggle" />
      <GaTabs v-if="layout.prefs.showTabs && !layout.maximized" />
      <main class="ga-layout__main">
        <RouterView v-slot="{ Component: page, route: r }">
          <!-- 页面切换：旧页淡出，新页淡入上移（D-030）；包装组件只有一个根元素，可以做过渡 -->
          <Transition name="ga-page" mode="out-in">
            <KeepAlive :include="layout.cachedNames">
              <component :is="wrap(page, String(r.name))" v-if="page" :key="`${r.fullPath}#${layout.refreshSeq[String(r.name)] ?? 0}`" />
            </KeepAlive>
          </Transition>
        </RouterView>
      </main>
      <button v-if="layout.maximized" type="button" class="ga-layout__restore" :title="t('shell.layout.restore')" data-test="layout-restore" @click="layout.maximized = false">
        <el-icon><Close /></el-icon>
      </button>
    </div>
  </div>
</template>
