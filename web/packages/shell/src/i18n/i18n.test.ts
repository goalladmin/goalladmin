import { describe, expect, it } from 'vitest'

import { createPortalI18n, resolveMessage } from './index'

describe('resolveMessage', () => {
  const msgs = {
    menu: { system: '系统管理', 'system.user': '用户管理', order: { list: '订单列表' } },
    perm: { system: { user: { list: '查看用户' } } },
    plain: '顶层',
  }
  it('目录键和带点的页面键可以并存', () => {
    expect(resolveMessage(msgs, 'menu.system')).toBe('系统管理')
    expect(resolveMessage(msgs, 'menu.system.user')).toBe('用户管理')
    expect(resolveMessage(msgs, 'menu.order.list')).toBe('订单列表')
    expect(resolveMessage(msgs, 'perm.system.user.list')).toBe('查看用户')
    expect(resolveMessage(msgs, 'plain')).toBe('顶层')
  })
  it('缺失返回 null', () => {
    expect(resolveMessage(msgs, 'menu.nope')).toBeNull()
    expect(resolveMessage(msgs, 'menu.system.user.deep')).toBeNull()
    expect(resolveMessage(msgs, 'x')).toBeNull()
    expect(resolveMessage(null, 'x')).toBeNull()
  })
})

describe('createPortalI18n', () => {
  it('端的文案与壳的深合并，端优先；t / te 走自定义解析', () => {
    const i18n = createPortalI18n({
      'zh-CN': { menu: { system: '系统', 'system.user': '用户' }, common: { search: '搜一下' } },
      'en-US': { menu: { system: 'System', 'system.user': 'Users' } },
    })
    const g = i18n.global
    g.locale.value = 'zh-CN'
    expect(g.t('menu.system')).toBe('系统')
    expect(g.t('menu.system.user')).toBe('用户')
    expect(g.t('common.search')).toBe('搜一下')
    expect(g.t('common.reset')).toBe('重置')
    expect(g.te('menu.system.user')).toBe(true)
    expect(g.te('menu.system.nope')).toBe(false)
    g.locale.value = 'en-US'
    expect(g.t('menu.system.user')).toBe('Users')
    expect(g.t('common.search')).toBe('Search')
    expect(g.t('shell.login.locked', { until: '10:00' })).toBe('Account locked. Try again after 10:00')
  })
})
