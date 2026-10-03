// 冒烟测试（规范 §13.3）：登录 → 强制改密 → 菜单按权限显示 → 数据中心、安全监控、工作台 → 建用户 → 建角色并授权 → 字典改显示后业务页面生效 →
// 菜单改名、建分组并拖入后侧边栏立即生效 → 开通代理商、商户（初始密码只显示一次） →
// 被授权用户登录只看到对应菜单 → 登出。全部走真实后端。
//
// 需要环境变量：GA_E2E_ADMIN（超管账号）、GA_E2E_PASSWORD（其初始密码，首次登录必须改）。

import { expect, test } from '@playwright/test'
import type { Page } from '@playwright/test'

const admin = process.env.GA_E2E_ADMIN ?? ''
const adminInitialPwd = process.env.GA_E2E_PASSWORD ?? ''
const stamp = Date.now().toString(36)
const adminPwd = `E2e-admin-${stamp}-pass`
const adminPwd2 = `E2e-admin-${stamp}-next`
const roleCode = `e2e-${stamp}`
const userName = `e2e_${stamp}`
const userPwd = `E2e-user-${stamp}-pass`
const userPwd2 = `E2e-user-${stamp}-next`

test.describe.configure({ mode: 'serial' })

test.beforeAll(() => {
  test.skip(!admin || !adminInitialPwd, '需要 GA_E2E_ADMIN / GA_E2E_PASSWORD（用 scripts/e2e.sh 运行）')
})

/** data-test 写在 el-input 上时会落到内层 <input>（Element Plus 透传 attrs），这里两种情况都兼容。 */
function input(page: Page, id: string) {
  return page.locator(`input[data-test="${id}"], [data-test="${id}"] input`).first()
}

async function login(page: Page, username: string, password: string) {
  await page.goto('/login')
  await input(page, 'login-username').fill(username)
  await input(page, 'login-password').fill(password)
  await page.getByTestId('login-submit').click()
}

async function changePassword(page: Page, oldPwd: string, newPwd: string) {
  await expect(page).toHaveURL(/\/change-password$/)
  await input(page, 'pwd-old').fill(oldPwd)
  await input(page, 'pwd-new').fill(newPwd)
  await input(page, 'pwd-confirm').fill(newPwd)
  await page.getByTestId('pwd-submit').click()
}

async function logout(page: Page) {
  await page.locator('.ga-topbar__user').click()
  await page.getByRole('menuitem', { name: '退出登录' }).click()
  await page.getByRole('button', { name: '确定' }).click()
  await expect(page).toHaveURL(/\/login$/)
}

function menuItem(page: Page, name: string) {
  const item = page.locator('.ga-sidebar').getByRole('menuitem', { name, exact: true })
  // 目录只点标题；整个 li 包含折叠动画中的子菜单，中心点可能落在别的菜单上。
  return item.locator(':scope > .el-sub-menu__title').or(item.and(page.locator('.el-menu-item')))
}

// 打开目录下的菜单：目录折叠着才点开它
async function openMenu(page: Page, dir: string, name: string) {
  if (!(await menuItem(page, name).isVisible())) await menuItem(page, dir).click()
  await menuItem(page, name).click()
}

test('超管：首次登录被要求改密，改完看到全部菜单', async ({ page }) => {
  await login(page, admin, adminInitialPwd)
  await changePassword(page, adminInitialPwd, adminPwd)
  // 默认落点：sort 最小的菜单页——根目录的数据中心（D-027、D-030、D-036），数字来自真实数据；首页标签固定在最前
  await expect(page).toHaveURL(/\/dashboard\/data-center$/)
  await expect(page.getByTestId('dash-card-users')).toBeVisible()
  await expect(page.getByTestId('dash-card-users').locator('.ga-count-up')).not.toHaveText('0')
  await expect(page.getByTestId('dash-trend').locator('canvas')).toBeVisible()
  await expect(page.getByTestId('tab-dashboard-data').locator('.ga-tabs__pin')).toBeVisible()
  await expect(menuItem(page, '数据中心')).toBeVisible()
  // 目录默认折叠，点开再看子项
  await menuItem(page, '权限管理').click()
  await expect(menuItem(page, '用户管理')).toBeVisible()
  await expect(menuItem(page, '角色管理')).toBeVisible()
  await expect(menuItem(page, '菜单管理')).toBeVisible()
  await expect(menuItem(page, '会话管理')).toHaveCount(0) // D-041：挪到运维中心
  // 安全监控在根目录、紧跟数据中心（D-041）；会话管理在运维中心
  await expect(menuItem(page, '安全监控')).toBeVisible()
  const order = await page.locator('.ga-sidebar .el-menu-item, .ga-sidebar .el-sub-menu__title').allInnerTexts()
  expect(order.findIndex((x) => x.includes('安全监控'))).toBe(order.findIndex((x) => x.includes('数据中心')) + 1)
  // 根目录的排序值（D-042）：数据中心、安全监控、工作台，然后是运维中心、权限管理、系统设置
  const at = (list: string[], name: string) => list.findIndex((x) => x.includes(name))
  const rootPairs: [string, string][] = [['工作台', '运维中心'], ['运维中心', '权限管理'], ['权限管理', '系统设置']]
  for (const [a, b] of rootPairs) {
    expect(at(order, a), `${a} 在 ${b} 前面`).toBeLessThan(at(order, b))
  }
  await menuItem(page, '运维中心').click()
  await expect(menuItem(page, '会话管理')).toBeVisible()
  // 运维中心里会话管理排在最后（D-042）
  const opsOrder = await page.locator('.ga-sidebar .el-menu-item').allInnerTexts()
  expect(at(opsOrder, '调查时间线')).toBeLessThan(at(opsOrder, '会话管理'))
  expect(at(opsOrder, '登录日志')).toBeLessThan(at(opsOrder, '错误日志'))
  // 侧栏一次只展开一个目录：再点开权限管理，下面切语言时要看"用户管理"
  await menuItem(page, '权限管理').click()
  await expect(menuItem(page, '用户管理')).toBeVisible()
  // 语言切换
  await page.getByTestId('lang-switch').click()
  await page.getByRole('menuitem', { name: 'English' }).click()
  await expect(menuItem(page, 'Users')).toBeVisible()
  // 11 种语言带国旗（D-026）：切到日语，菜单、按钮跟着变；繁体没有旗帜而是"繁"字标
  await expect(page.getByTestId('lang-switch').locator('img.ga-flag')).toHaveAttribute('alt', 'English')
  await page.getByTestId('lang-switch').click()
  await expect(page.getByTestId('lang-zh-TW').locator('.ga-flag--badge')).toHaveText('繁')
  await expect(page.getByTestId('lang-ta-IN').locator('img.ga-flag')).toBeVisible()
  await page.getByRole('menuitem', { name: '日本語' }).click()
  await expect(menuItem(page, 'ユーザー管理')).toBeVisible()
  await expect(page.locator('html')).toHaveAttribute('lang', 'ja-JP')
  await page.getByTestId('lang-switch').click()
  await page.getByRole('menuitem', { name: '简体中文' }).click()
  await expect(menuItem(page, '用户管理')).toBeVisible()
  // 暗色模式
  await page.getByTestId('toggle-dark').click()
  await expect(page.locator('html')).toHaveClass(/dark/)
  await page.getByTestId('toggle-dark').click()
  await expect(page.locator('html')).not.toHaveClass(/dark/)
  // 页面刷新后登录态通过刷新凭证恢复（访问令牌只在内存里）
  await menuItem(page, '用户管理').click()
  await expect(page).toHaveURL(/\/system\/users$/)
  await page.reload()
  await expect(page.getByTestId('user-table')).toBeVisible()
  await expect(page).toHaveURL(/\/system\/users$/)  // 运维中心（D-032）：操作日志、登录日志在这里
  await menuItem(page, '运维中心').click()
  await expect(menuItem(page, '操作日志')).toBeVisible()
  await expect(menuItem(page, '登录日志')).toBeVisible()
  // 系统设置（D-034）：字典管理挪到这里，旁边是只读的安全设置（同一时间只展开一个目录，所以放在最后）
  await menuItem(page, '系统设置').click()
  await expect(menuItem(page, '字典管理')).toBeVisible()
  await expect(menuItem(page, '安全设置')).toBeVisible()
})

test('超管：数据中心、安全监控、工作台都是真实数据，数字跳到终值', async ({ page }) => {
  await login(page, admin, adminPwd)
  await expect(page).toHaveURL(/\/dashboard\/data-center$/)
  // 数字从 0 跳到终值：动画结束后显示的正好是接口给的值
  const users = page.getByTestId('dash-card-users').locator('.ga-count-up')
  const target = Number(await users.getAttribute('data-value'))
  expect(target).toBeGreaterThan(0)
  await expect(users).toHaveText(new Intl.NumberFormat('zh-CN').format(target))
  await expect(page.getByTestId('dash-top-users')).toContainText(admin)
  await expect(page.getByTestId('dash-hours').locator('canvas')).toBeVisible()
  await page.getByTestId('dash-range').getByText('近 90 天').click()
  await expect(page.getByTestId('dash-card-ops')).toContainText('近 90 天')

  // 安全监控（D-041 在根目录）：安全 + 服务器
  await menuItem(page, '安全监控').click()
  await expect(page).toHaveURL(/\/dashboard\/monitor$/)
  await expect(page.getByTestId('mon-recent')).toContainText(admin)
  await expect(page.getByTestId('mon-card-uptime')).toContainText('秒')
  await expect(page.getByTestId('mon-db')).toContainText('正常')
  await expect(page.getByTestId('mon-routes')).toContainText('GET')
  await expect(page.getByTestId('mon-requests').locator('canvas')).toBeVisible()

  // 工作台：本人的数据，快捷入口能打开页面
  await menuItem(page, '工作台').click()
  await expect(page).toHaveURL(/\/dashboard\/workspace$/)
  await expect(page.getByTestId('ws-greeting')).toContainText(admin)
  await expect(page.getByTestId('ws-devices')).toContainText('当前设备')
  await expect(page.getByTestId('ws-recent')).toContainText('修改密码')
  // 快捷入口按菜单顺序取前 8 个：根目录的数据中心、安全监控之后是代理商管理、商户管理（D-066，排序 100、110），
  // 运维中心排不进前 8 个；菜单名有包含关系（"操作日志"和"商户操作日志"），按全名点
  const shortcuts = page.getByTestId('ws-shortcuts')
  await expect(shortcuts.getByText('操作日志', { exact: true })).toHaveCount(0)
  await shortcuts.getByText('商户列表', { exact: true }).click()
  await expect(page).toHaveURL(/\/merchant\/list$/)
  await logout(page)
})

test('超管：运维中心——伪造令牌记为安全事件，从 IP 进入调查时间线，查看本身也留痕', async ({ page }) => {
  // 带一个伪造的令牌调接口：401，同时记一条"无效令牌"
  const res = await page.request.get('/api/platform/v1/system/users', { headers: { Authorization: 'Bearer forged.token.value' } })
  expect(res.status()).toBe(401)

  await login(page, admin, adminPwd)
  await expect(page).toHaveURL(/\/dashboard\/data-center$/)
  await openMenu(page, '运维中心', '安全事件')
  await expect(page).toHaveURL(/\/ops\/security-events$/)
  const row = page.getByTestId('secevent-table').locator('tr', { hasText: '无效令牌' }).first()
  await expect(row).toBeVisible()

  // 点 IP 进入时间线：同一 IP 上的登录和这条安全事件串在一起
  await row.getByTestId('secevent-ip').click()
  await expect(page).toHaveURL(/\/ops\/timeline\?ip=/)
  const list = page.getByTestId('timeline-list')
  await expect(list).toContainText('无效令牌')
  await expect(list).toContainText('登录成功')

  // 错误日志页能打开（没有故障时是空表）
  await menuItem(page, '错误日志').click()
  await expect(page).toHaveURL(/\/ops\/error-logs$/)
  await expect(page.getByTestId('errorlog-table')).toBeVisible()

  // 刚才的查看都记进了操作日志
  await menuItem(page, '操作日志').click()
  await expect(page.getByTestId('oplog-table')).toContainText('查看安全事件')
  await expect(page.getByTestId('oplog-table')).toContainText('查看调查时间线')
  await logout(page)
})

test('超管：建角色并授权，建用户并分配角色', async ({ page }) => {
  await login(page, admin, adminPwd)
  await expect(page).toHaveURL(/\/dashboard\/data-center$/)

  // 角色
  await menuItem(page, '权限管理').click()
  await menuItem(page, '角色管理').click()
  await expect(page).toHaveURL(/\/system\/roles$/)
  await page.getByTestId('role-create').click()
  await input(page, 'role-form-code').fill(roleCode)
  await input(page, 'role-form-name').fill(`角色 ${stamp}`)
  await page.getByTestId('role-form-submit').click()
  const row = page.getByTestId('role-table').locator('tr', { hasText: roleCode })
  await expect(row).toBeVisible()
  await row.getByTestId('role-grant').click()
  await expect(page.getByTestId('grant-dialog')).toBeVisible()
  await page.getByTestId('perm-system:user:list').click()
  await page.getByTestId('perm-system:user:update').click()
  await page.getByTestId('grant-submit').click()
  await expect(page.getByTestId('grant-dialog')).toBeHidden()

  // 用户
  await menuItem(page, '用户管理').click()
  await page.getByTestId('user-create').click()
  await input(page, 'user-form-username').fill(userName)
  await input(page, 'user-form-password').fill(userPwd)
  await page.getByTestId('user-form-roles').click()
  await page.getByRole('option', { name: `角色 ${stamp}` }).click()
  await page.keyboard.press('Escape')
  await page.getByTestId('user-form-submit').click()
  await expect(page.getByTestId('user-table').locator('tr', { hasText: userName })).toBeVisible()

  // 重置密码只有超管能做，超管自己（包括所有超管）的密码不能在后台重置，只能用命令行（D-035）
  await expect(page.getByTestId(`user-actions-${userName}`).getByTestId('user-reset-password')).toBeEnabled()
  const selfReset = page.getByTestId(`user-actions-${admin}`).getByTestId('user-reset-password')
  await expect(selfReset).toBeDisabled()
  await expect(selfReset).toHaveAttribute('title', /admin reset-password/)

  // 操作日志里能看到刚才的动作
  await openMenu(page, '运维中心', '操作日志')
  await expect(page.getByTestId('oplog-table')).toContainText('创建用户')
  await expect(page.getByTestId('oplog-table')).toContainText('角色授权')
  await logout(page)
})

test('超管：角色的数据权限在授权对话框的第二个页签，和功能权限一起保存（D-039）', async ({ page }) => {
  await login(page, admin, adminPwd)
  await openMenu(page, '权限管理', '角色管理')
  const row = page.getByTestId('role-table').locator('tr', { hasText: roleCode })
  await row.getByTestId('role-grant').click()
  const dialog = page.getByTestId('grant-dialog')
  await expect(dialog).toBeVisible()
  await dialog.getByRole('tab', { name: '数据权限' }).click()
  const res = page.getByTestId('data-system:user')
  await expect(res).toContainText('用户与会话')
  await expect(res).toContainText('默认：仅本人')
  // 新建的角色取默认值"仅本人"
  await expect(page.getByTestId('scope-system:user-self')).toHaveClass(/is-checked/)
  await page.getByTestId('scope-system:user-dept_tree').click()
  await page.getByTestId('grant-submit').click()
  await expect(dialog).toBeHidden()
  // 再打开：范围和功能权限都还在
  await row.getByTestId('role-grant').click()
  await expect(dialog).toBeVisible()
  await expect(page.getByTestId('perm-system:user:list')).toHaveClass(/is-checked/)
  await dialog.getByRole('tab', { name: '数据权限' }).click()
  await expect(page.getByTestId('scope-system:user-dept_tree')).toHaveClass(/is-checked/)
  await page.keyboard.press('Escape')
  await logout(page)
})

test('超管：部门和岗位——建部门树和岗位，建用户时选部门和岗位，按上级部门筛选能看到下级部门的人', async ({ page }) => {
  await login(page, admin, adminPwd)
  await expect(page).toHaveURL(/\/dashboard\/data-center$/)

  // 部门：建一个顶级部门，再在它下面加一个下级
  await openMenu(page, '权限管理', '部门管理')
  await expect(page).toHaveURL(/\/system\/depts$/)
  await page.getByTestId('dept-create').click()
  await input(page, 'dept-form-name').fill(`总部${stamp}`)
  await page.getByTestId('dept-form-submit').click()
  const top = page.getByTestId('dept-table').locator('tr', { hasText: `总部${stamp}` })
  await expect(top).toBeVisible()
  await top.getByRole('button', { name: '新增下级' }).click()
  await input(page, 'dept-form-name').fill(`研发${stamp}`)
  await page.getByTestId('dept-form-submit').click()
  await expect(page.getByTestId('dept-table').locator('tr', { hasText: `研发${stamp}` })).toBeVisible()

  // 岗位
  await menuItem(page, '岗位管理').click()
  await expect(page).toHaveURL(/\/system\/posts$/)
  await page.getByTestId('post-create').click()
  await input(page, 'post-form-code').fill(`pm-${stamp}`)
  await input(page, 'post-form-name').fill(`产品${stamp}`)
  await page.getByTestId('post-form-submit').click()
  await expect(page.getByTestId('post-table')).toContainText(`产品${stamp}`)

  // 建用户时选下级部门和岗位
  await menuItem(page, '用户管理').click()
  await page.getByTestId('user-create').click()
  await input(page, 'user-form-username').fill(`org_${stamp}`)
  await page.getByTestId('user-form-dept').first().click()
  await page.locator('.el-popper:visible').getByText(`研发${stamp}`, { exact: true }).click()
  await page.getByTestId('user-form-posts').click()
  await page.getByRole('option', { name: `产品${stamp}` }).click()
  await page.keyboard.press('Escape')
  await page.getByTestId('user-form-submit').click()
  await expect(page.getByTestId('password-reveal')).toBeVisible()
  await page.getByTestId('password-reveal').getByRole('button', { name: '关闭' }).last().click()
  const row = page.getByTestId('user-table').locator('tr', { hasText: `org_${stamp}` })
  await expect(row).toContainText(`研发${stamp}`)
  await expect(row).toContainText(`产品${stamp}`)

  // 按上级部门筛选：下级部门的人也在
  await page.getByTestId('user-filter-dept').first().click()
  await page.locator('.el-popper:visible').getByText(`总部${stamp}`, { exact: true }).click()
  await page.getByRole('button', { name: '查询' }).click()
  await expect(page.getByTestId('user-table').locator('tbody tr')).toHaveCount(1)
  await expect(row).toBeVisible()
  await logout(page)
})

test('超管：字典管理——建字典、加项、改显示文字、删项、删字典', async ({ page }) => {
  const code = `e2e_${stamp}`
  await login(page, admin, adminPwd)
  await expect(page).toHaveURL(/\/dashboard\/data-center$/)
  await openMenu(page, '系统设置', '字典管理')
  await expect(page).toHaveURL(/\/system\/dicts$/)

  await page.getByTestId('dict-create').click()
  await input(page, 'dict-form-code').fill(code)
  await input(page, 'dict-form-name').fill(`字典 ${stamp}`)
  await page.getByTestId('dict-form-submit').click()
  const row = page.getByTestId('dict-table').locator('tr', { hasText: code })
  await expect(row).toBeVisible()
  await row.click()

  const items = page.getByTestId('dict-item-table')
  await page.getByTestId('dict-item-create').click()
  await input(page, 'dict-item-value').fill('high')
  await input(page, 'dict-item-label').fill('高')
  await page.getByTestId('dict-item-submit').click()
  await expect(items).toContainText('高')

  await items.locator('tr', { hasText: '高' }).getByRole('button', { name: '编辑' }).click()
  await input(page, 'dict-item-label').fill('紧急')
  await page.getByTestId('dict-item-submit').click()
  await expect(items).toContainText('紧急')

  // 删项、删字典，库回到初始状态（冒烟测试可以反复跑）
  await items.locator('tr', { hasText: '紧急' }).getByRole('button', { name: '删除' }).click()
  await page.getByRole('button', { name: '确定' }).click()
  await expect(items).not.toContainText('紧急')
  await page.locator('.ga-page').getByRole('button', { name: '删除' }).first().click()
  await page.getByRole('button', { name: '确定' }).click()
  await expect(page.getByTestId('dict-table').locator('tr', { hasText: code })).toHaveCount(0)
  await logout(page)
})

test('超管：安全设置——只读展示生效的策略、范围和配置项，没有保存入口', async ({ page }) => {
  await login(page, admin, adminPwd)
  await expect(page).toHaveURL(/\/dashboard\/data-center$/)
  await openMenu(page, '系统设置', '安全设置')
  await expect(page).toHaveURL(/\/settings\/security$/)
  await expect(page.getByTestId('security-hint')).toContainText('只能在服务器的配置文件里修改')
  await expect(page.getByTestId('security-hint')).toContainText('platform')

  // 五个页签，与配置文件里的五类策略一一对应
  const tabs = page.getByTestId('security-tabs')
  for (const name of ['验证码', '密码复杂度', '限流', '失败锁定', '密码有效期']) await expect(tabs.getByRole('tab', { name })).toBeVisible()

  // 验证码：默认失败 3 次后要，每次都要默认关闭
  const captcha = page.getByTestId('security-table-captcha')
  await expect(captcha.getByTestId('policy-captchaAfterFailures')).toHaveText('3 次')
  await expect(captcha.getByTestId('policy-captchaAlways')).toHaveText('未开启')
  await expect(captcha).toContainText('portals.platform.login.captchaAfterFailures')
  await expect(captcha).toContainText('1 次 – 5 次')
  await expect(page.getByTestId('security-fixed-captcha')).toContainText('5 位数字')

  // 失败锁定：时长按分钟显示，来源是默认值
  await tabs.getByRole('tab', { name: '失败锁定' }).click()
  const lock = page.getByTestId('security-table-lock')
  await expect(lock.getByTestId('policy-lockAfterFailures')).toHaveText('10 次')
  await expect(lock.getByTestId('policy-lockDuration')).toHaveText('15 分钟')
  await expect(lock.locator('tr', { hasText: '锁定时长' })).toContainText('1 分钟 – 24 小时')
  await expect(lock.locator('tr', { hasText: '锁定时长' })).toContainText('默认值')
  await expect(lock.getByTestId('policy-syntax-lockDuration')).toContainText('lockDuration: 15m') // 配置里时长的写法
  await expect(page.getByTestId('security-fixed-lock')).toContainText('没有关闭开关')

  // 密码有效期：默认不过期；强制改密始终生效
  await tabs.getByRole('tab', { name: '密码有效期' }).click()
  await expect(page.getByTestId('policy-maxAgeDays')).toHaveText('不过期')
  await expect(page.getByTestId('security-fixed-expiry')).toContainText('首次登录必须先修改密码')

  // 只读：页面上没有任何输入框和保存按钮
  await expect(page.locator('.ga-page').getByRole('button', { name: '保存' })).toHaveCount(0)
  await expect(page.locator('.ga-page input')).toHaveCount(0)

  await logout(page)
})

test('超管：IP 访问控制——封禁和解除、不能封自己、白名单不含自己的 IP 时保存被拒、账号白名单设了再清空（D-062）', async ({ page }) => {
  // 每次运行用不同的文档地址，测试库不清库
  const victim = `198.51.100.${(Date.now() % 250) + 1}`
  await login(page, admin, adminPwd)
  await expect(page).toHaveURL(/\/dashboard\/data-center$/)
  await openMenu(page, '系统设置', 'IP 访问控制')
  await expect(page).toHaveURL(/\/settings\/ip$/)

  // 黑名单：封自己当前的 IP 被拒绝，对话框里说明原因
  await page.getByTestId('ip-block-open').click()
  await input(page, 'ip-block-cidr').fill('127.0.0.1')
  await page.getByTestId('ip-block-submit').click()
  await expect(page.getByTestId('ip-block-error')).toContainText('把你当前的 IP 挡在外面')
  // 封一个别的地址：出现在列表里，有到期时间；再解除
  await input(page, 'ip-block-cidr').fill(victim)
  await page.getByTestId('ip-block-submit').click()
  await expect(page.getByTestId('ip-block-error')).toBeHidden()
  const row = page.getByTestId('ip-deny-table').locator('tr', { hasText: victim })
  await expect(row).toBeVisible()
  await expect(row).not.toContainText('永久')
  await row.getByTestId('ip-unblock').click()
  await page.getByRole('button', { name: '确定' }).click()
  await expect(row).toHaveCount(0)

  // 平台白名单：显示当前 IP；只填别的地址保存会被拒绝（会把自己挡在外面），名单保持为空
  await page.getByTestId('ip-tabs').getByRole('tab', { name: '平台白名单' }).click()
  const allow = page.getByTestId('ip-allow-editor').first()
  await expect(allow.getByTestId('ip-your')).toHaveText('127.0.0.1')
  await allow.getByTestId('ip-add-row').click()
  await input(page, 'ip-cidr-0').fill('10.9.8.7')
  await page.getByTestId('ip-allow-save').click()
  await expect(page.getByTestId('ip-allow-error')).toContainText('把你当前的 IP 挡在外面')
  await page.getByRole('button', { name: '重置' }).last().click()
  await expect(allow).toContainText('没有设置：不限制来源')

  // 账号白名单：给被授权用户设一条，重新打开能看到；再清空（后面的用例要用这个账号从本机登录）
  await openMenu(page, '权限管理', '用户管理')
  await page.getByPlaceholder('账号 / 显示名 / 邮箱').fill(userName)
  await page.getByPlaceholder('账号 / 显示名 / 邮箱').press('Enter')
  await page.getByTestId(`user-actions-${userName}`).getByTestId('user-ip-allow').click()
  const dialog = page.getByTestId('user-ip-dialog')
  await expect(dialog).toBeVisible()
  await dialog.getByTestId('ip-add-row').click()
  await input(page, 'ip-cidr-0').fill('10.9.8.0/24')
  await page.getByTestId('user-ip-save').click()
  await expect(dialog).toBeHidden()
  await page.getByTestId(`user-actions-${userName}`).getByTestId('user-ip-allow').click()
  await expect(input(page, 'ip-cidr-0')).toHaveValue('10.9.8.0/24')
  await dialog.getByRole('button', { name: '删除' }).click()
  await page.getByTestId('user-ip-save').click()
  await expect(dialog).toBeHidden()

  // 操作日志里有这些动作
  await openMenu(page, '运维中心', '操作日志')
  for (const name of ['封禁 IP', '解除 IP 封禁', '设置账号 IP 白名单']) await expect(page.getByTestId('oplog-table')).toContainText(name)
  await logout(page)
})

test('超管：代理商、商户管理——开通时初始密码只显示一次，商户挂在代理商下，详情里看账号、重置主账号密码，停用，按编号看日志（D-065、D-066）', async ({ page }) => {
  const agentName = `E2E代理${stamp}`
  const merchantName = `E2E商户${stamp}`
  await login(page, admin, adminPwd)
  await expect(page).toHaveURL(/\/dashboard\/data-center$/)
  // 两个目录在工作台之后、运维中心之前（根目录排序 100、110）
  const order = await page.locator('.ga-sidebar .el-menu-item, .ga-sidebar .el-sub-menu__title').allInnerTexts()
  const at = (name: string) => order.findIndex((x) => x.includes(name))
  expect(at('工作台')).toBeLessThan(at('代理商管理'))
  expect(at('代理商管理')).toBeLessThan(at('商户管理'))
  expect(at('商户管理')).toBeLessThan(at('运维中心'))

  // 开通代理商：编号 A 加 8 位数字，主账号的初始密码只在这个对话框里出现
  await openMenu(page, '代理商管理', '代理商列表')
  await expect(page).toHaveURL(/\/agent\/list$/)
  // 两个列表页的按钮 data-test 相同：按文字点，切页的过渡动画里不会点到上一页的
  await page.getByRole('button', { name: '开通代理商' }).click()
  await input(page, 'org-form-name').fill(agentName)
  await input(page, 'org-form-owner').fill('boss')
  await page.getByTestId('org-form-submit').click()
  const reveal = page.getByTestId('org-password').last()
  await expect(reveal).toBeVisible()
  const agentCode = (await reveal.getByTestId('org-password-code').innerText()).trim()
  expect(agentCode).toMatch(/^A\d{8}$/)
  const agentPwd = (await reveal.getByTestId('org-password-value').innerText()).trim()
  expect(agentPwd).toHaveLength(20)
  await reveal.getByRole('button', { name: '关闭', exact: true }).click()
  await expect(page.getByTestId('org-table')).toContainText(agentCode)
  await expect(page.getByTestId('org-table')).not.toContainText(agentPwd)

  // 开通商户，挂在刚才的代理商下
  await openMenu(page, '商户管理', '商户列表')
  await expect(page).toHaveURL(/\/merchant\/list$/)
  await page.getByRole('button', { name: '开通商户' }).click()
  await input(page, 'org-form-name').fill(merchantName)
  await page.getByTestId('org-form-agent').click()
  await page.getByTestId('org-form-agent').locator('input').fill(agentCode)
  await page.locator('.el-select-dropdown__item:visible', { hasText: agentCode }).click()
  await input(page, 'org-form-owner').fill('admin')
  await page.getByTestId('org-form-submit').click()
  await expect(reveal).toBeVisible()
  const merchantCode = (await reveal.getByTestId('org-password-code').innerText()).trim()
  expect(merchantCode).toMatch(/^M\d{8}$/)
  const firstPwd = (await reveal.getByTestId('org-password-value').innerText()).trim()
  await reveal.getByRole('button', { name: '关闭', exact: true }).click()
  const row = page.getByTestId('org-table').locator('tr', { hasText: merchantCode })
  await expect(row).toContainText(agentName)
  await expect(row).toContainText('admin')

  // 详情：账号页签里主账号带标记；重置主账号密码，新密码同样只显示一次
  await page.getByTestId(`org-actions-${merchantCode}`).getByTestId('org-detail-open').click()
  const drawer = page.getByTestId('org-detail')
  await expect(drawer).toContainText(merchantCode)
  await drawer.getByRole('tab', { name: '账号' }).click()
  await expect(drawer.getByTestId('org-accounts')).toContainText('admin')
  await expect(drawer.getByTestId('org-accounts')).toContainText('主账号')
  await drawer.getByTestId('org-reset-owner').click()
  await page.getByRole('button', { name: '确定' }).click()
  const reset = page.getByTestId('org-password').last()
  await expect(reset).toBeVisible()
  const resetPwd = (await reset.getByTestId('org-password-value').innerText()).trim()
  expect(resetPwd).toHaveLength(20)
  expect(resetPwd).not.toBe(firstPwd)
  await reset.getByRole('button', { name: '关闭', exact: true }).click()
  // 从详情去这个商户的登录日志：编号已经填好
  await drawer.getByRole('button', { name: '登录日志' }).click()
  await expect(page).toHaveURL(new RegExp(`/merchant/login-logs\\?orgCode=${merchantCode}$`))
  await expect(input(page, 'orglog-code')).toHaveValue(merchantCode)
  await expect(page.getByTestId('orglog-table')).toBeVisible()

  // 停用商户
  await openMenu(page, '商户管理', '商户列表')
  await page.getByTestId(`org-actions-${merchantCode}`).getByTestId('org-toggle').click()
  await page.getByRole('button', { name: '确定' }).click()
  await expect(page.getByTestId('org-table').locator('tr', { hasText: merchantCode })).toContainText('停用')

  // 运维中心的安全事件可以选端（D-066）
  await openMenu(page, '运维中心', '安全事件')
  await page.getByTestId('ops-portal').click()
  await page.locator('.el-select-dropdown__item:visible', { hasText: '商户端' }).click()
  await expect(page.getByTestId('ops-portal')).toContainText('商户端')

  // 平台的操作日志里有这些动作，请求体里没有初始密码
  await openMenu(page, '运维中心', '操作日志')
  for (const name of ['开通代理商', '开通商户', '重置商户主账号密码', '启停商户']) await expect(page.getByTestId('oplog-table')).toContainText(name)
  await expect(page.getByTestId('oplog-table')).not.toContainText(resetPwd)
  await logout(page)
})

test('超管：菜单管理——改名、建分组并拖进去后侧边栏立即生效，删除分组和恢复默认后回到原样', async ({ page }) => {
  // 菜单树变长了（运维中心 D-032，代理商管理、商户管理 D-066）：窗口够高，拖动时源和目标都在视口里，不用边拖边滚动
  await page.setViewportSize({ width: 1280, height: 1600 })
  await login(page, admin, adminPwd)
  await expect(page).toHaveURL(/\/dashboard\/data-center$/)
  const sidebar = page.locator('.ga-sidebar')
  await expect(sidebar).toContainText('岗位管理')

  await menuItem(page, '权限管理').click()
  await menuItem(page, '菜单管理').click()
  await expect(page).toHaveURL(/\/system\/menus$/)
  const tree = page.getByTestId('menu-tree')
  const post = page.getByTestId('menu-node-system-post')
  await expect(post).toContainText('/system/posts')
  await expect(post).toContainText('system:post:list')

  // 改显示名：路径、页面、权限码只读展示
  await post.getByRole('button', { name: '编辑' }).click()
  await expect(page.getByRole('dialog')).toContainText('以上由代码决定，不能修改')
  await input(page, 'menu-form-zh-CN').fill(`我的岗位${stamp}`)
  await page.getByTestId('menu-form-submit').click()
  await expect(post).toContainText(`我的岗位${stamp}`)
  await expect(post).toContainText('已调整')
  await expect(sidebar).toContainText(`我的岗位${stamp}`)

  // 建分组：空分组不出现在侧边栏
  await page.getByTestId('menu-create-group').click()
  await input(page, 'menu-form-zh-CN').fill(`常用${stamp}`)
  await page.getByTestId('menu-form-submit').click()
  const group = tree.locator('.el-tree-node__content', { hasText: `常用${stamp}` })
  await expect(group).toBeVisible()
  await expect(sidebar).not.toContainText(`常用${stamp}`)

  // 把岗位管理拖进分组，保存排序：侧边栏出现分组，页面照常打开
  await post.dragTo(group)
  await expect(tree.locator('.el-tree-node', { hasText: `常用${stamp}` }).getByTestId('menu-node-system-post')).toBeVisible()
  await page.getByTestId('menu-save-layout').click()
  await expect(page.getByTestId('menu-save-layout')).toBeDisabled()
  await expect(sidebar).toContainText(`常用${stamp}`)
  await sidebar.getByText(`常用${stamp}`, { exact: true }).click()
  await menuItem(page, `我的岗位${stamp}`).click()
  await expect(page).toHaveURL(/\/system\/posts$/)
  await expect(page.getByTestId('post-table')).toBeVisible()

  // 删除分组：岗位管理回到"权限管理"目录；恢复默认：名字回到"岗位管理"
  await menuItem(page, '权限管理').click()
  await menuItem(page, '菜单管理').click()
  await group.getByRole('button', { name: '删除' }).click()
  await page.getByRole('button', { name: '确定' }).click()
  await expect(group).toHaveCount(0)
  await post.getByRole('button', { name: '恢复默认' }).click()
  await page.getByRole('button', { name: '确定' }).click()
  await expect(post).toContainText('岗位管理')
  await expect(post).not.toContainText('已调整')
  await expect(sidebar).not.toContainText(`常用${stamp}`)
  await expect(sidebar).not.toContainText(`我的岗位${stamp}`)

  // 直接填排序值：用户管理排到权限管理的最后，侧边栏随之变化；恢复默认后回到第一个
  const sysItems = sidebar.locator('.el-sub-menu', { hasText: '权限管理' }).locator('.el-menu-item')
  await expect(sysItems.first()).toContainText('用户管理')
  const users = page.getByTestId('menu-node-system-user')
  await expect(page.getByTestId('menu-sort-system-user')).toHaveText('#10')
  await users.getByRole('button', { name: '编辑' }).click()
  await input(page, 'menu-form-sort').fill('999')
  await page.getByTestId('menu-form-submit').click()
  await expect(page.getByTestId('menu-sort-system-user')).toHaveText('#999')
  await expect(sysItems.last()).toContainText('用户管理')
  await users.getByRole('button', { name: '恢复默认' }).click()
  await page.getByRole('button', { name: '确定' }).click()
  await expect(page.getByTestId('menu-sort-system-user')).toHaveText('#10')
  await expect(sysItems.first()).toContainText('用户管理')
  await logout(page)
})

test('超管：全局搜索、偏好设置、锁屏在服务端生效', async ({ page }) => {
  await login(page, admin, adminPwd)
  await expect(page).toHaveURL(/\/dashboard\/data-center$/)

  // ⌘K / Ctrl+K 搜索菜单，回车打开
  await page.keyboard.press('Control+k')
  await expect(input(page, 'search-input')).toBeFocused()
  await input(page, 'search-input').fill('字典')
  await expect(page.getByTestId('search-results')).toContainText('字典管理')
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(/\/system\/dicts$/)

  // 偏好设置：关掉标签栏再打开
  await page.getByTestId('open-prefs').click()
  await page.getByTestId('prefs-tabs').click()
  await expect(page.locator('.ga-tabs')).toHaveCount(0)
  await page.getByTestId('prefs-tabs').click()
  await expect(page.locator('.ga-tabs')).toHaveCount(1)
  await page.keyboard.press('Escape')

  // 锁屏：刷新页面也还锁着（锁在服务端的会话上）
  await page.getByTestId('lock-screen').click()
  await expect(page.getByTestId('lock-screen-overlay')).toBeVisible()
  // 锁屏层下面不留页面：布局和业务数据都已卸载
  await expect(page.locator('.ga-layout')).toHaveCount(0)
  await expect(page.getByTestId('dict-table')).toHaveCount(0)
  await page.reload()
  await expect(page.getByTestId('lock-screen-overlay')).toBeVisible()
  await expect(page.locator('.ga-layout')).toHaveCount(0)
  // 输错提示剩余次数，输对解锁并回到原来的页面
  await input(page, 'unlock-password').fill('wrong-password')
  await page.getByTestId('unlock-submit').click()
  await expect(page.getByTestId('unlock-error')).toContainText('4')
  await input(page, 'unlock-password').fill(adminPwd)
  await page.getByTestId('unlock-submit').click()
  await expect(page.getByTestId('lock-screen-overlay')).toHaveCount(0)
  await expect(page).toHaveURL(/\/system\/dicts$/)
  await expect(page.getByTestId('dict-table')).toBeVisible()
  await logout(page)
})

test('超管：个人中心——改资料后顶栏立即更新，安全设置显示真实状态，下线其他设备保留当前会话，改密后用新密码登录', async ({ page, browser }) => {
  await login(page, admin, adminPwd)
  await expect(page).toHaveURL(/\/dashboard\/data-center$/)
  await page.locator('.ga-topbar__user').click()
  await page.getByRole('menuitem', { name: '个人中心' }).click()
  await expect(page).toHaveURL(/\/profile$/)

  // 基本设置：改显示名和简介，顶栏和左侧名片立即是新名字；用户名只读
  const newName = `管理员 ${stamp}`
  await expect(input(page, 'profile-display-name')).toHaveValue(admin)
  await input(page, 'profile-display-name').fill(newName)
  await page.locator('textarea[data-test="profile-bio"], [data-test="profile-bio"] textarea').first().fill('第一行\n第二行')
  await page.getByTestId('profile-save').click()
  await expect(page.locator('.ga-topbar__username')).toHaveText(newName)
  await expect(page.getByTestId('profile-name')).toHaveText(newName)
  await page.reload()
  await expect(input(page, 'profile-display-name')).toHaveValue(newName)

  // 安全设置：只有真实状态，没有开关；另一处登录后会话数变 2，下线其他设备后回到 1，另一处被踢出、这里照常
  await page.getByTestId('profile-nav-security').click()
  await expect(page.getByTestId('profile-sessions')).toContainText('个设备')
  await expect(page.locator('.ga-profile__list .el-switch')).toHaveCount(0)
  // 前面的用例可能还留着超管的会话，所以只看相对变化：另一处登录后加一，下线其他设备后只剩本设备
  const before = Number(/(\d+) 个设备/.exec((await page.getByTestId('profile-sessions').textContent()) ?? '')?.[1] ?? '0')
  expect(before).toBeGreaterThanOrEqual(1)
  const other = await browser.newContext()
  const otherPage = await other.newPage()
  await login(otherPage, admin, adminPwd)
  await expect(otherPage).toHaveURL(/\/dashboard\/data-center$/)
  await page.reload()
  await page.getByTestId('profile-nav-security').click()
  await expect(page.getByTestId('profile-sessions')).toContainText(`${before + 1} 个设备`)
  await page.getByTestId('profile-revoke-others').click()
  await page.getByRole('button', { name: '确定' }).click()
  await expect(page.getByTestId('profile-sessions')).toContainText('1 个设备')
  await expect(page.getByTestId('profile-revoke-others')).toBeDisabled()
  await otherPage.goto('/system/users')
  await expect(otherPage).toHaveURL(/\/login/)
  await other.close()

  // 新消息提醒：只是本地占位，关掉一个再刷新还记得
  await page.getByTestId('profile-nav-notify').click()
  await expect(page.getByText('暂不生效')).toBeVisible()
  await expect(page.getByTestId('profile-notify-system')).toHaveClass(/is-checked/)
  await page.getByTestId('profile-notify-system').click()
  await page.reload()
  await page.getByTestId('profile-nav-notify').click()
  await expect(page.getByTestId('profile-notify-system')).not.toHaveClass(/is-checked/)

  // 修改密码：在个人中心改完还留在当前会话；登出后旧密码不能登录，新密码可以
  await page.getByTestId('profile-nav-password').click()
  await input(page, 'profile-pwd-old').fill(adminPwd)
  await input(page, 'profile-pwd-new').fill(adminPwd2)
  await input(page, 'profile-pwd-confirm').fill(adminPwd2)
  await page.getByTestId('profile-pwd-submit').click()
  await expect(page.getByText('密码已修改')).toBeVisible()
  await expect(page).toHaveURL(/\/profile$/)
  await page.getByTestId('profile-nav-security').click()
  await expect(page.getByTestId('profile-sessions')).toContainText('1 个设备')
  await logout(page)
  await login(page, admin, adminPwd)
  await expect(page.getByTestId('login-error')).toContainText('账号或密码错误')
  await login(page, admin, adminPwd2)
  await expect(page).toHaveURL(/\/dashboard\/data-center$/)
  await logout(page)
})

test('超管：头像——没有头像时是账号首字母加彩色底，能选内置头像、上传图片（裁剪后上传）、恢复默认，顶栏跟着变（D-040）', async ({ page }) => {
  await login(page, admin, adminPwd2)
  await expect(page).toHaveURL(/\/dashboard\/data-center$/)
  const top = page.locator('.ga-topbar__user .el-avatar')
  await expect(top).toHaveText(admin.slice(0, 1).toUpperCase())
  const bg = await top.evaluate((el) => getComputedStyle(el).backgroundColor)
  expect(bg).not.toMatch(/rgba?\(0, 0, 0|rgba?\(255, 255, 255/)
  await page.locator('.ga-topbar__user').click()
  await page.getByRole('menuitem', { name: '个人中心' }).click()

  // 选内置头像
  await page.getByTestId('profile-avatar-change').click()
  const dialog = page.getByTestId('avatar-dialog')
  await expect(dialog).toBeVisible()
  await dialog.getByRole('tab', { name: '内置头像' }).click()
  await page.getByTestId('avatar-preset-ocean').click()
  await page.getByTestId('avatar-save').click()
  await expect(dialog).toBeHidden()
  await expect(top.locator('img')).toHaveAttribute('src', /^data:image\/svg\+xml/)

  // 上传图片：浏览器裁剪后上传，服务器重新生成 JPEG；刷新后还在
  await page.getByTestId('profile-avatar-change').click()
  await dialog.getByRole('tab', { name: '上传图片' }).click()
  const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==', 'base64')
  await page.getByTestId('avatar-file').setInputFiles({ name: 'me.png', mimeType: 'image/png', buffer: png })
  await expect(page.getByTestId('avatar-save')).toBeEnabled()
  await page.getByTestId('avatar-save').click()
  await expect(dialog).toBeHidden()
  await expect(top.locator('img')).toHaveAttribute('src', /^data:image\/jpeg;base64,/)
  await page.reload()
  await expect(page.locator('.ga-topbar__user .el-avatar img')).toHaveAttribute('src', /^data:image\/jpeg;base64,/)

  // 不是图片的文件：对话框里提示，不上传
  await page.getByTestId('profile-avatar-change').click()
  await page.getByTestId('avatar-file').setInputFiles({ name: 'x.txt', mimeType: 'text/plain', buffer: Buffer.from('hello') })
  await expect(page.getByTestId('avatar-error')).toContainText('不是能识别的图片')

  // 恢复默认：回到首字母
  await page.getByTestId('avatar-clear').click()
  await expect(dialog).toBeHidden()
  await expect(page.locator('.ga-topbar__user .el-avatar img')).toHaveCount(0)
  await expect(page.locator('.ga-topbar__user .el-avatar')).toHaveText(admin.slice(0, 1).toUpperCase())
  await logout(page)
})

test('被授权用户：改密后只看到被授权的菜单和按钮，未授权页面是 404', async ({ page }) => {
  await login(page, userName, userPwd)
  await changePassword(page, userPwd, userPwd2)
  await expect(page).toHaveURL(/\/system\/users$/)
  await menuItem(page, '权限管理').click()
  await expect(menuItem(page, '用户管理')).toBeVisible()
  await expect(menuItem(page, '角色管理')).toHaveCount(0)
  await expect(menuItem(page, '操作日志')).toHaveCount(0)
  await expect(menuItem(page, '系统设置')).toHaveCount(0) // 没有字典和安全设置的权限，整个目录都不显示
  // 按钮权限：没有 system:user:create
  await menuItem(page, '用户管理').click()
  await expect(page.getByTestId('user-table')).toBeVisible()
  await expect(page.getByTestId('user-create')).toHaveCount(0)
  await expect(page.getByTestId('user-reset-password')).toHaveCount(0) // 不是超管就没有重置密码
  // 有 system:user:update：能编辑自己看得到的用户
  await expect(page.getByTestId(`user-actions-${userName}`).getByRole('button', { name: '编辑' })).toBeVisible()
  // 未授权的页面没有路由 → 404
  await page.goto('/system/roles')
  await expect(page.getByRole('main').getByText('页面不存在', { exact: true })).toBeVisible()
  await page.goto('/settings/security')
  await expect(page.getByRole('main').getByText('页面不存在', { exact: true })).toBeVisible()
  await logout(page)
  // 登出后刷新凭证已吊销：直接访问受保护页面回到登录页
  await page.goto('/system/users')
  await expect(page).toHaveURL(/\/login/)
})

test('登录失败提示统一，错三次要验证码', async ({ page }) => {
  await login(page, userName, 'wrong-password-1')
  await expect(page.getByTestId('login-error')).toContainText('账号或密码错误')
  await input(page, 'login-password').fill('wrong-password-2')
  await page.getByTestId('login-submit').click()
  await expect(page.getByTestId('login-error')).toBeVisible()
  await input(page, 'login-password').fill('wrong-password-3')
  await page.getByTestId('login-submit').click()
  await expect(input(page, 'login-captcha')).toBeVisible()
})
