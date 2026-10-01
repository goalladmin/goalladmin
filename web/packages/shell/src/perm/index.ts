// 按钮级权限（规范 §6.6）：hasPerm()、v-perm 指令、<GaPerm>。只影响显示，安全以后端守卫为准。

import { defineComponent } from 'vue'
import type { Directive, DirectiveBinding, PropType } from 'vue'

import { useAuthStore } from '../auth/store'

export type PermMode = 'any' | 'all'
export type PermValue = string | string[]

/** 当前用户是否拥有权限码（超管恒为 true）。 */
export function hasPerm(codes: PermValue, mode: PermMode = 'any'): boolean {
  return useAuthStore().hasPerm(codes, mode)
}

function allowed(binding: DirectiveBinding<PermValue>): boolean {
  const mode: PermMode = binding.arg === 'all' ? 'all' : 'any'
  return hasPerm(binding.value ?? [], mode)
}

/**
 * v-perm="'system:user:create'"、v-perm="['a', 'b']"（任一）、v-perm:all="['a', 'b']"（全部）。
 * 没有权限时把元素从 DOM 移除。权限只在挂载和更新时评估；登录态变化会重建页面，所以够用。
 */
export const vPerm: Directive<HTMLElement, PermValue> = {
  mounted(el, binding) {
    if (!allowed(binding)) el.parentNode?.removeChild(el)
  },
  updated(el, binding) {
    if (!allowed(binding)) el.parentNode?.removeChild(el)
  },
}

/** <GaPerm perm="system:user:create">…</GaPerm>，perm 也可以是数组；mode 默认 any。 */
export const GaPerm = defineComponent({
  name: 'GaPerm',
  props: {
    perm: { type: [String, Array] as PropType<PermValue>, required: true },
    mode: { type: String as PropType<PermMode>, default: 'any' },
  },
  setup(props, { slots }) {
    return () => (hasPerm(props.perm, props.mode) ? (slots.default?.() ?? null) : null)
  },
})
