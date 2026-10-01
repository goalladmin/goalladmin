// 菜单图标按名字解析（后端 MenuNode.Icon 用的是 Element Plus 图标名，如 Setting、User）。
// 映射表里没有的名字不渲染图标，不报错。

import * as Icons from '@element-plus/icons-vue'
import type { Component } from 'vue'

const table = Icons as unknown as Record<string, Component>

export function resolveIcon(name?: string): Component | null {
  if (!name) return null
  return table[name] ?? null
}

/** 全部可用的图标名（菜单管理的图标选择器用），按字母排序。后端只校验格式，渲染时查这张表，查不到就不显示。 */
export function iconNames(): string[] {
  return Object.keys(table)
    .filter((k) => /^[A-Z][A-Za-z0-9]*$/.test(k))
    .sort()
}
