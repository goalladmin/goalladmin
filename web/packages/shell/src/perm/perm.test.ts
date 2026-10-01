// 规范 §13.3：权限指令。
/* eslint-disable vue/one-component-per-file */

import { beforeEach, describe, expect, it } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { defineComponent, h } from 'vue'
import { mount } from '@vue/test-utils'

import { GaPerm, hasPerm, vPerm } from './index'
import { useAuthStore } from '../auth/store'

function login(perms: string[], superUser = false) {
  const auth = useAuthStore()
  auth.user = { id: 1, username: 'u', displayName: 'U', avatar: '', mustChangePwd: false, super: superUser }
  auth.perms = perms
}

const Page = defineComponent({
  directives: { perm: vPerm },
  components: { GaPerm },
  template: `
    <div>
      <button id="create" v-perm="'system:user:create'">create</button>
      <button id="any" v-perm="['system:user:create', 'system:user:list']">any</button>
      <button id="all" v-perm:all="['system:user:create', 'system:user:list']">all</button>
      <GaPerm perm="system:role:grant"><span id="grant">grant</span></GaPerm>
      <GaPerm :perm="['system:user:list']"><span id="list">list</span></GaPerm>
    </div>`,
})

describe('perm', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('hasPerm：any / all / 超管', () => {
    login(['a:b:c'])
    expect(hasPerm('a:b:c')).toBe(true)
    expect(hasPerm('x:y:z')).toBe(false)
    expect(hasPerm(['x:y:z', 'a:b:c'])).toBe(true)
    expect(hasPerm(['x:y:z', 'a:b:c'], 'all')).toBe(false)
    expect(hasPerm([])).toBe(true)
    login([], true)
    expect(hasPerm('anything:at:all')).toBe(true)
  })

  it('v-perm 没有权限时移除元素；GaPerm 不渲染插槽', () => {
    login(['system:user:list'])
    const w = mount(Page)
    expect(w.find('#create').exists()).toBe(false)
    expect(w.find('#any').exists()).toBe(true)
    expect(w.find('#all').exists()).toBe(false)
    expect(w.find('#grant').exists()).toBe(false)
    expect(w.find('#list').exists()).toBe(true)
  })

  it('超管全部可见', () => {
    login([], true)
    const w = mount(Page)
    for (const id of ['#create', '#any', '#all', '#grant', '#list']) expect(w.find(id).exists()).toBe(true)
  })

  it('未登录什么都不显示', () => {
    const w = mount(defineComponent({ components: { GaPerm }, render: () => h(GaPerm, { perm: 'a:b:c' }, () => h('i', { id: 'x' })) }))
    expect(w.find('#x').exists()).toBe(false)
  })
})
