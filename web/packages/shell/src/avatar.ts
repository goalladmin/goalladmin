// 头像（D-040）：用户的 avatar 只会是三种值——空（显示名字首字母）、preset:<名字>（这里的内置头像）、
// upload:<键>（本人上传，按键向服务器要图）。内置头像是本项目原创的几何图案，直接在浏览器里生成 SVG，不发请求。
//
// 名字表和后端（server/modules/system/avatar.go 的 avatarPresets）是同一份，由那边的测试对齐。

import type { Client } from './request/client'
import { Codes, isApiError } from './types'

export interface AvatarPreset {
  name: string
  /** 渐变的两端颜色。 */
  from: string
  to: string
  /** 叠在渐变上的白色图案（SVG 片段，64×64 画布）。 */
  motif: string
}

const W = '#fff'

export const AVATAR_PRESETS: readonly AvatarPreset[] = [
  { name: 'aurora', from: '#4f46e5', to: '#06b6d4', motif: `<path d="M0 38C14 28 30 48 64 32V64H0Z" fill="${W}" fill-opacity=".32"/><path d="M0 48C20 40 40 58 64 46V64H0Z" fill="${W}" fill-opacity=".22"/>` },
  { name: 'ocean', from: '#0ea5e9', to: '#1e3a8a', motif: `<g fill="none" stroke="${W}" stroke-opacity=".35" stroke-width="3"><circle cx="32" cy="32" r="8"/><circle cx="32" cy="32" r="16"/><circle cx="32" cy="32" r="24"/></g>` },
  { name: 'forest', from: '#16a34a', to: '#064e3b', motif: `<path d="M8 50L22 22L36 50Z" fill="${W}" fill-opacity=".3"/><path d="M28 50L44 16L60 50Z" fill="${W}" fill-opacity=".22"/>` },
  { name: 'sunset', from: '#f97316', to: '#be123c', motif: `<circle cx="32" cy="36" r="13" fill="${W}" fill-opacity=".35"/><g stroke="${W}" stroke-opacity=".28" stroke-width="3"><path d="M6 46H58"/><path d="M12 53H52"/></g>` },
  { name: 'berry', from: '#db2777', to: '#6d28d9', motif: `<g fill="${W}" fill-opacity=".3"><circle cx="18" cy="18" r="5"/><circle cx="46" cy="18" r="5"/><circle cx="32" cy="32" r="5"/><circle cx="18" cy="46" r="5"/><circle cx="46" cy="46" r="5"/></g>` },
  { name: 'slate', from: '#64748b', to: '#1e293b', motif: `<g stroke="${W}" stroke-opacity=".22" stroke-width="5"><path d="M-8 24L24 -8"/><path d="M-8 48L48 -8"/><path d="M-8 72L72 -8"/><path d="M16 72L72 16"/><path d="M40 72L72 40"/></g>` },
  { name: 'sand', from: '#eab308', to: '#b45309', motif: `<path d="M0 44C18 34 30 40 42 36S58 30 64 32V64H0Z" fill="${W}" fill-opacity=".3"/><path d="M0 54C16 48 34 56 64 48V64H0Z" fill="${W}" fill-opacity=".22"/>` },
  { name: 'mint', from: '#2dd4bf', to: '#0f766e', motif: `<path d="M32 12L52 32L32 52L12 32Z" fill="${W}" fill-opacity=".3"/><path d="M32 22L42 32L32 42L22 32Z" fill="${W}" fill-opacity=".3"/>` },
  { name: 'coral', from: '#fb7185', to: '#ea580c', motif: `<g fill="${W}" fill-opacity=".28"><rect x="12" y="12" width="18" height="18" rx="4"/><rect x="34" y="34" width="18" height="18" rx="4"/></g><rect x="34" y="12" width="18" height="18" rx="4" fill="${W}" fill-opacity=".14"/>` },
  { name: 'violet', from: '#8b5cf6', to: '#312e81', motif: `<circle cx="32" cy="32" r="20" fill="none" stroke="${W}" stroke-opacity=".3" stroke-width="3"/><circle cx="46" cy="18" r="6" fill="${W}" fill-opacity=".45"/>` },
  { name: 'sky', from: '#38bdf8', to: '#6366f1', motif: `<g fill="${W}" fill-opacity=".35"><circle cx="24" cy="36" r="9"/><circle cx="36" cy="30" r="12"/><circle cx="46" cy="38" r="8"/><rect x="18" y="36" width="34" height="10" rx="5"/></g>` },
  { name: 'amber', from: '#f59e0b', to: '#92400e', motif: `<g fill="${W}" fill-opacity=".28"><path d="M32 10L41 15V25L32 30L23 25V15Z"/><path d="M20 32L29 37V47L20 52L11 47V37Z"/><path d="M44 32L53 37V47L44 52L35 47V37Z"/></g>` },
]

const presetByName = new Map(AVATAR_PRESETS.map((p) => [p.name, p]))

/** 内置头像的 SVG（data: 地址）；不认识的名字返回 undefined。 */
export function presetAvatarSrc(name: string): string | undefined {
  const p = presetByName.get(name)
  if (!p) return undefined
  const svg =
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">` +
    `<defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="${p.from}"/><stop offset="1" stop-color="${p.to}"/></linearGradient></defs>` +
    `<rect width="64" height="64" fill="url(#g)"/>${p.motif}</svg>`
  return `data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`
}

export type AvatarRef = { kind: 'none' } | { kind: 'preset'; name: string } | { kind: 'upload'; key: string }

const uploadKeyRe = /^[0-9a-f]{32}$/

/** 解析 avatar 值。别的写法（例如老数据里的外部地址）一律当作没有头像：不拿任意地址当图片加载。 */
export function parseAvatar(value: string | null | undefined): AvatarRef {
  if (!value) return { kind: 'none' }
  if (value.startsWith('preset:') && presetByName.has(value.slice(7))) return { kind: 'preset', name: value.slice(7) }
  if (value.startsWith('upload:') && uploadKeyRe.test(value.slice(7))) return { kind: 'upload', key: value.slice(7) }
  return { kind: 'none' }
}

// 上传的头像：按键缓存（键变了图才会变），同一张图只请求一次。读取失败时记 null，显示首字母。
const cache = new Map<string, Promise<string | null>>()

/** 按键取上传的头像；small 时取 64×64 的小图。 */
export function loadUploadedAvatar(client: Client, path: string, key: string, small: boolean): Promise<string | null> {
  const id = `${path}|${key}|${small ? 64 : 256}`
  let p = cache.get(id)
  if (!p) {
    p = client
      .get<{ image: string }>(`${path}/${key}`, { params: small ? { size: 64 } : undefined, silent: true })
      .then((r) => (typeof r.image === 'string' && r.image.startsWith('data:image/jpeg;base64,') ? r.image : null))
      .catch((e: unknown) => {
        // 键已失效（404）就记住"没有"，不再反复请求；别的失败（锁屏中、网络断了）不记，下次再试
        if (!(isApiError(e) && e.code === Codes.NotFound)) cache.delete(id)
        return null
      })
    cache.set(id, p)
  }
  return p
}

/**
 * 没有头像时首字母圆圈的配色（D-040）：20 个色系（含蓝灰、灰、暖灰三个灰色系）× 5 档深浅 = 100 组 [底色, 文字色]。
 * 在感知均匀的 OKLCH 色彩空间里取色；浅的几档配同色系的深色字，深的几档配白字；每组底色和文字的对比度
 * 都不低于 4.5:1（WCAG AA），底色都不是黑色或白色。按账号固定挑一组：同一个人每次都一样，不同的人分散开。
 */
export const AVATAR_COLORS: readonly (readonly [bg: string, fg: string])[] = [
  // 红
  ['#fed2cd', '#7e312e'], ['#ffaba3', '#7e312e'], ['#ff726b', '#641919'], ['#d33a3c', '#ffffff'], ['#a20118', '#ffffff'],
  // 橙
  ['#ffd4bd', '#7a3705'], ['#ffae81', '#7a3705'], ['#f67f2f', '#5a2702'], ['#be5904', '#ffffff'], ['#873d02', '#ffffff'],
  // 琥珀
  ['#fed7a1', '#694501'], ['#f4b85d', '#694501'], ['#da950b', '#503402'], ['#a46e01', '#120900'], ['#734c02', '#ffffff'],
  // 黄
  ['#ebe09c', '#584e03'], ['#d7c65c', '#584e03'], ['#bba60a', '#433b01'], ['#8b7b02', '#100d00'], ['#615604', '#ffffff'],
  // 黄绿
  ['#cfeaa1', '#405603'], ['#afd466', '#405603'], ['#8bb705', '#304202'], ['#678806', '#0d1500'], ['#475f02', '#ffffff'],
  // 浅绿
  ['#bbeeb5', '#245b1f'], ['#8fda87', '#245b1f'], ['#5ebe55', '#0e4709'], ['#2e9226', '#011a01'], ['#0a6802', '#ffffff'],
  // 绿
  ['#baedc3', '#0f5d2b'], ['#8ed99d', '#0f5d2b'], ['#5bbd74', '#01481d'], ['#2a904b', '#001a06'], ['#04672d', '#ffffff'],
  // 青绿
  ['#b1ece3', '#035a52'], ['#7ed7cb', '#035a52'], ['#3dbbae', '#01453f'], ['#018d82', '#001613'], ['#05635b', '#ffffff'],
  // 青
  ['#abebf6', '#025763'], ['#72d5e6', '#025763'], ['#1db8ce', '#01434c'], ['#028a9b', '#011519'], ['#07606c', '#ffffff'],
  // 天蓝
  ['#bbe5fe', '#035476'], ['#7dcffe', '#035476'], ['#3fb1ea', '#00405b'], ['#0584b8', '#00121d'], ['#045c82', '#ffffff'],
  // 蓝
  ['#cddffe', '#2b4b87'], ['#a5c5fe', '#2b4b87'], ['#72a3fe', '#193772'], ['#3f75dd', '#000827'], ['#194cb1', '#ffffff'],
  // 靛蓝
  ['#d6dcfe', '#414587'], ['#b6bffe', '#414587'], ['#909afe', '#2f3171'], ['#666adc', '#ffffff'], ['#4442b0', '#ffffff'],
  // 紫罗兰
  ['#e0d8fe', '#523f81'], ['#c8b7ff', '#523f81'], ['#ad8dfe', '#402b6c'], ['#855dd7', '#ffffff'], ['#6034ac', '#ffffff'],
  // 紫
  ['#f0d1fe', '#633875'], ['#e4a9ff', '#633875'], ['#ce7df1', '#4c225d'], ['#a151c2', '#ffffff'], ['#792798', '#ffffff'],
  // 粉
  ['#fecee8', '#74315a'], ['#fea2d7', '#74315a'], ['#ed74bd', '#5c1a44'], ['#bc4891', '#ffffff'], ['#911b6a', '#ffffff'],
  // 玫红
  ['#ffd0d6', '#7c2f3f'], ['#fea8b5', '#7c2f3f'], ['#fe6d8b', '#63182b'], ['#cd3e62', '#ffffff'], ['#9f043e', '#ffffff'],
  // 棕
  ['#f3d8c6', '#68432a'], ['#e2bba2', '#68432a'], ['#c8997b', '#533118'], ['#9b6f52', '#170600'], ['#734a2e', '#ffffff'],
  // 蓝灰
  ['#d7dfe9', '#434e5c'], ['#bac5d4', '#434e5c'], ['#98a6b8', '#313b49'], ['#6f7c8c', '#060e19'], ['#4a5666', '#ffffff'],
  // 灰
  ['#dedede', '#4d4d4d'], ['#c4c4c4', '#4d4d4d'], ['#a4a4a4', '#3a3a3a'], ['#7a7a7a', '#0d0d0d'], ['#555555', '#ffffff'],
  // 暖灰
  ['#e1ddd9', '#514c47'], ['#c8c3be', '#514c47'], ['#aaa39d', '#3e3a35'], ['#7f7973', '#100d08'], ['#5a544e', '#ffffff'],
]

/** 按账号挑一组配色：FNV-1a 散列取模，结果稳定。 */
export function avatarColor(seed: string | null | undefined): { bg: string; fg: string } {
  let h = 0x811c9dc5
  for (const ch of seed ?? '') {
    h ^= ch.codePointAt(0)!
    h = Math.imul(h, 0x01000193) >>> 0
  }
  const [bg, fg] = AVATAR_COLORS[h % AVATAR_COLORS.length]!
  return { bg, fg }
}

/** 名字的首字母（没有头像时显示）。 */
export function avatarInitial(name: string | null | undefined): string {
  const s = (name ?? '').trim()
  return s ? Array.from(s)[0]!.toUpperCase() : '?'
}
