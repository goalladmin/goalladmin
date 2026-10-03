// 代理商端、商户端的冒烟测试（规范 §13.3，D-067）：平台开主体 → 主账号用编号首次登录、被要求改密 → 建角色和员工 →
// 员工登录只看到授权的菜单；代理商登录看到名下商户、看不到别的商户。全部走真实后端（scripts/e2e.sh 起的三个程序）。
//
// 需要环境变量：GA_E2E_ADMIN2（平台超管）、GA_E2E_PASSWORD2（其初始密码）。用另一个超管：平台的冒烟会改第一个超管的密码。

import { expect, test } from '@playwright/test'
import type { Page } from '@playwright/test'

const admin = process.env.GA_E2E_ADMIN2 ?? ''
const adminInitialPwd = process.env.GA_E2E_PASSWORD2 ?? ''
const agentWeb = `http://127.0.0.1:${process.env.GA_E2E_AGENT_WEB_PORT ?? 4175}`
const merchantWeb = `http://127.0.0.1:${process.env.GA_E2E_MERCHANT_WEB_PORT ?? 4176}`
const stamp = Date.now().toString(36)
const adminPwd = `E2e-portal-${stamp}-pass`
const ownerPwd = `E2e-owner-${stamp}-pass`
const staffPwd = `E2e-staff-${stamp}-pass`
const staffPwd2 = `E2e-staff-${stamp}-next`

test.describe.configure({ mode: 'serial' })

test.beforeAll(() => {
  test.skip(!admin || !adminInitialPwd, '需要 GA_E2E_ADMIN2 / GA_E2E_PASSWORD2（用 scripts/e2e.sh 运行）')
})

/** data-test 写在 el-input 上时会落到内层 <input>（Element Plus 透传 attrs），这里两种情况都兼容。 */
function input(page: Page, id: string) {
  return page.locator(`input[data-test="${id}"], [data-test="${id}"] input`).first()
}

function menuItem(page: Page, name: string) {
  const item = page.locator('.ga-sidebar').getByRole('menuitem', { name, exact: true })
  // 目录只点标题；整个 li 包含折叠动画中的子菜单，中心点可能落在别的菜单上。
  return item.locator(':scope > .el-sub-menu__title').or(item.and(page.locator('.el-menu-item')))
}

async function openMenu(page: Page, dir: string, name: string) {
  if (!(await menuItem(page, name).isVisible())) await menuItem(page, dir).click()
  await menuItem(page, name).click()
}

/** 两个主体端都使用真实接口验证数据中心和独立安全页面（D-090、D-091）。 */
async function ownerDashboard(page: Page) {
  await expect(page).toHaveURL(/\/data-center$/)
  await expect(page.locator('.ga-sidebar .el-menu-item').first()).toHaveText('数据中心')
  await expect(page.getByTestId('dash-card-users').locator('.ga-count-up')).toHaveText('1')
  await expect(page.getByTestId('dash-top-users')).toContainText('boss')
  await expect(page.getByTestId('dash-trend').locator('canvas')).toBeVisible()
  await menuItem(page, '概览').click()
  await expect(page).toHaveURL(/\/overview$/)
  await expect(page.getByTestId('org-ip-allow')).toHaveCount(0)
}

async function ownerIPSettings(page: Page) {
  const rules = [
    ['116.88.8.*', '116.88.8.0/24'],
    ['116.88.*.*', '116.88.0.0/16'],
    ['116.*.*.*', '116.0.0.0/8'],
  ] as const
  await openMenu(page, '安全设置', 'IP 白名单')
  await expect(page).toHaveURL(/\/security\/ip-allow$/)
  const allow = page.getByTestId('org-ip-allow')
  await expect(allow).toBeVisible()
  // 保留当前测试 IP，确认三种通配写法能保存并在重新登录态恢复后读取。
  await page.getByTestId('org-ip-add-mine').click()
  const mine = await input(page, 'org-ip-cidr-0').inputValue()
  const saved = [`${mine}/${mine.includes(':') ? 128 : 32}`, ...rules.map(([, cidr]) => cidr)].sort()
  for (const [i, [wildcard]] of rules.entries()) {
    await allow.getByRole('button', { name: '添加一行', exact: true }).click()
    await input(page, `org-ip-cidr-${i + 1}`).fill(wildcard)
  }
  await page.getByTestId('org-ip-save').click()
  for (const [i, cidr] of saved.entries()) await expect(input(page, `org-ip-cidr-${i}`)).toHaveValue(cidr)
  await page.reload()
  for (const [i, cidr] of saved.entries()) await expect(input(page, `org-ip-cidr-${i}`)).toHaveValue(cidr)
  for (let i = 0; i < rules.length + 1; i++) await allow.getByRole('button', { name: '删除', exact: true }).first().click()
  const cleared = page.waitForResponse((r) => r.url().endsWith('/org/ip-allow') && r.request().method() === 'PUT')
  await page.getByTestId('org-ip-save').click()
  expect((await (await cleared).json()).code).toBe(0)

  await openMenu(page, '安全设置', 'IP 黑名单')
  await expect(page).toHaveURL(/\/security\/ip-deny$/)
  for (const [wildcard, normalized] of rules) {
    await page.getByTestId('org-deny-add').click()
    await input(page, 'org-deny-cidr').fill(wildcard)
    await page.getByTestId('org-deny-save').click()
    const row = page.getByTestId('org-deny-table').locator('tr', { hasText: normalized })
    await expect(row).toBeVisible()
    await row.getByTestId('org-deny-remove').click()
    await page.getByRole('button', { name: '确定', exact: true }).click()
    await expect(row).toHaveCount(0)
  }
}

async function changePassword(page: Page, oldPwd: string, newPwd: string) {
  await expect(page).toHaveURL(/\/change-password$/)
  await input(page, 'pwd-old').fill(oldPwd)
  await input(page, 'pwd-new').fill(newPwd)
  await input(page, 'pwd-confirm').fill(newPwd)
  await page.getByTestId('pwd-submit').click()
}

/** 主体端登录：编号 + 账号 + 密码。 */
async function orgLogin(page: Page, web: string, code: string, username: string, password: string) {
  await page.goto(`${web}/login`)
  await input(page, 'login-org').fill(code)
  await input(page, 'login-username').fill(username)
  await input(page, 'login-password').fill(password)
  await page.getByTestId('login-submit').click()
}

async function logout(page: Page) {
  await page.locator('.ga-topbar__user').click()
  await page.getByRole('menuitem', { name: '退出登录' }).click()
  await page.getByRole('button', { name: '确定' }).click()
  await expect(page).toHaveURL(/\/login$/)
}

/** 平台端开一个代理商（kind = agent）或商户（merchant）：返回编号和主账号的初始密码。 */
async function openOrg(page: Page, kind: 'agent' | 'merchant', name: string, agentCode?: string) {
  const label = kind === 'agent' ? '代理商' : '商户'
  await openMenu(page, `${label}管理`, `${label}列表`)
  await expect(page).toHaveURL(new RegExp(`/${kind}/list$`))
  await page.getByRole('button', { name: `开通${label}` }).click()
  await input(page, 'org-form-name').fill(name)
  if (agentCode) {
    await page.getByTestId('org-form-agent').click()
    await page.getByTestId('org-form-agent').locator('input').fill(agentCode)
    await page.locator('.el-select-dropdown__item:visible', { hasText: agentCode }).click()
  }
  await input(page, 'org-form-owner').fill('boss')
  await page.getByTestId('org-form-submit').click()
  const reveal = page.getByTestId('org-password').last()
  await expect(reveal).toBeVisible()
  const code = (await reveal.getByTestId('org-password-code').innerText()).trim()
  const password = (await reveal.getByTestId('org-password-value').innerText()).trim()
  await reveal.getByRole('button', { name: '关闭', exact: true }).click()
  await expect(page.getByTestId('org-table')).toContainText(code)
  return { code, password }
}

let merchantCode = ''

test('商户端：平台开商户，主账号用编号首次登录改密，建角色和员工，员工登录只看到授权的菜单（D-067）', async ({ page }) => {
  // 平台：新超管首次登录改密，开一个商户
  await page.goto('/login')
  await input(page, 'login-username').fill(admin)
  await input(page, 'login-password').fill(adminInitialPwd)
  await page.getByTestId('login-submit').click()
  await changePassword(page, adminInitialPwd, adminPwd)
  await expect(page).toHaveURL(/\/dashboard\/data-center$/)
  const merchantName = `E2E商户端${stamp}`
  const m = await openOrg(page, 'merchant', merchantName)
  merchantCode = m.code

  // 商户端：编号 + 账号 + 初始密码登录，被要求改密；改完落在数据中心，顶栏显示商户名称
  await orgLogin(page, merchantWeb, m.code.toLowerCase(), 'boss', m.password)
  await changePassword(page, m.password, ownerPwd)
  await ownerDashboard(page)
  await expect(page.getByTestId('topbar-org')).toHaveText(merchantName)
  await expect(page.getByTestId('org-code')).toHaveText(m.code)
  await expect(page.getByTestId('org-owner-tag')).toBeVisible()
  await expect(page.getByTestId('org-count-accounts')).toContainText('1')
  await ownerIPSettings(page)

  // 建角色"收银员"，只授"查看子账号"
  await openMenu(page, '账号与权限', '角色与权限')
  await page.getByTestId('org-role-create').click()
  await input(page, 'org-role-form-code').fill('cashier')
  await input(page, 'org-role-form-name').fill('收银员')
  await page.getByTestId('org-role-form-submit').click()
  const roleRow = page.getByTestId('org-role-table').locator('tr', { hasText: 'cashier' })
  await expect(roleRow).toBeVisible()
  await roleRow.getByTestId('org-role-grant').click()
  const grant = page.getByTestId('org-grant-dialog')
  await expect(grant.getByTestId('org-perm-merchant:account:list')).toBeVisible()
  // 敏感权限码带标记；主账号能授
  await expect(grant.getByTestId('org-perm-merchant:account:create')).toContainText('敏感')
  await grant.getByTestId('org-perm-merchant:account:list').click()
  await grant.getByTestId('org-grant-submit').click()
  await expect(grant).toBeHidden()

  // 建员工并顺手分配角色：自己给了密码，就不弹一次性密码
  await openMenu(page, '账号与权限', '子账号')
  await page.getByTestId('org-account-create').click()
  await input(page, 'org-account-form-username').fill('cashier1')
  await input(page, 'org-account-form-password').fill(staffPwd)
  await page.getByTestId('org-account-form-roles').click()
  await page.locator('.el-select-dropdown__item:visible', { hasText: '收银员' }).click()
  await page.getByTestId('org-account-form-submit').click()
  const staffRow = page.getByTestId('org-account-table').locator('tr', { hasText: 'cashier1' })
  await expect(staffRow).toContainText('收银员')
  // 主账号这一行带标记，自己停不了自己
  await expect(page.getByTestId('org-account-table').locator('tr', { hasText: 'boss' })).toContainText('主账号')
  await logout(page)

  // 员工：首次登录改密，只看到概览和"子账号"，没有新建按钮
  await orgLogin(page, merchantWeb, m.code, 'cashier1', staffPwd)
  await changePassword(page, staffPwd, staffPwd2)
  await expect(page).toHaveURL(/\/overview$/)
  await expect(page.getByTestId('org-owner-tag')).toHaveCount(0)
  await expect(page.getByTestId('org-ip-allow')).toHaveCount(0)
  await expect(menuItem(page, '概览')).toBeVisible()
  await expect(menuItem(page, '数据中心')).toHaveCount(0)
  await expect(menuItem(page, '安全设置')).toHaveCount(0)
  await expect(menuItem(page, '日志')).toHaveCount(0)
  await menuItem(page, '账号与权限').click()
  await expect(menuItem(page, '子账号')).toBeVisible()
  await expect(menuItem(page, '角色与权限')).toHaveCount(0)
  await expect(menuItem(page, '在线会话')).toHaveCount(0)
  await menuItem(page, '子账号').click()
  await expect(page.getByTestId('org-account-table')).toContainText('cashier1')
  await expect(page.getByTestId('org-account-create')).toHaveCount(0)
  // 没授权的页面是 404
  for (const path of ['/access/roles', '/data-center', '/security/ip-allow', '/security/ip-deny']) {
    await page.goto(`${merchantWeb}${path}`)
    await expect(page.locator('.el-result__title')).toHaveText('页面不存在')
  }
  await logout(page)
})

test('代理商端：代理商登录看到名下商户、看不到别的商户（D-067）', async ({ page }) => {
  await page.goto('/login')
  await input(page, 'login-username').fill(admin)
  await input(page, 'login-password').fill(adminPwd)
  await page.getByTestId('login-submit').click()
  await expect(page).toHaveURL(/\/dashboard\/data-center$/)
  const agentName = `E2E代理端${stamp}`
  const a = await openOrg(page, 'agent', agentName)
  const child = await openOrg(page, 'merchant', `E2E名下商户${stamp}`, a.code)

  await orgLogin(page, agentWeb, a.code, 'boss', a.password)
  await changePassword(page, a.password, ownerPwd)
  await ownerDashboard(page)
  await expect(page.getByTestId('topbar-org')).toHaveText(agentName)
  await expect(page.getByTestId('org-count-merchants')).toContainText('1')
  await ownerIPSettings(page)
  await menuItem(page, '名下商户').click()
  const table = page.getByTestId('agent-merchant-table')
  await expect(table).toContainText(child.code)
  await expect(table).not.toContainText(merchantCode) // 上一条用例的直属商户
  // 只读：没有新建、编辑、停用的按钮
  await expect(page.getByRole('button', { name: '新建' })).toHaveCount(0)
  await logout(page)
})
