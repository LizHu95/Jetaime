import { expect, test, type Page } from '@playwright/test'

const user = { id: 'browser-test-user', username: 'Demo_User', nickname: '小夏' }
const token = 'x'.repeat(43)
async function mockAuth(page: Page) {
  await page.route('**/api/auth/**', async route => {
    const path = new URL(route.request().url()).pathname
    if (path.endsWith('/register')) {
      const body = route.request().postDataJSON()
      expect(Object.keys(body).sort()).toEqual(['nickname', 'password', 'username'])
      await route.fulfill({ status: 201, json: { ...user, username: body.username } })
    } else if (path.endsWith('/login')) {
      const body = route.request().postDataJSON()
      if (body.password === 'wrong-password') {
        await route.fulfill({ status: 401, json: { error: { code: 'invalid_credentials' } } })
      } else {
        await route.fulfill({ json: { user, token, tokenType: 'Bearer', expiresAt: new Date(Date.now() + 60_000).toISOString() } })
      }
    } else {
      expect(route.request().headers().authorization).toBe(`Bearer ${token}`)
      await route.fulfill({ status: 204 })
    }
  })
}

test('register → login → reload → logout', async ({ page }) => {
  await mockAuth(page)
  await page.goto('/page/auth/register/')
  await page.getByLabel('账号', { exact: true }).fill('Demo_User')
  await page.getByLabel('昵称（可选）').fill('小夏')
  await page.getByLabel('密码', { exact: true }).fill('sample-password')
  await page.getByLabel('确认密码', { exact: true }).fill('sample-password')
  await page.getByRole('button', { name: '创建账号', exact: true }).click()
  await expect(page).toHaveURL(/\/page\/auth\/login\/$/)
  await expect(page.getByRole('status')).toContainText('注册成功')
  await expect(page.getByLabel('账号', { exact: true })).toHaveValue('Demo_User')
  await expect(page.getByLabel('密码', { exact: true })).toHaveValue('')
  await page.getByLabel('密码', { exact: true }).fill('sample-password')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.getByRole('heading', { name: '你好，小夏' })).toBeVisible()
  await page.reload()
  await expect(page.getByRole('heading', { name: '你好，小夏' })).toBeVisible()
  await page.getByRole('button', { name: '退出当前账号' }).click()
  await expect(page).toHaveURL(/\/auth\/login\/$/)
  expect(await page.evaluate(() => sessionStorage.getItem('jetaime.auth.session'))).toBeNull()
})

test('validates empty fields, password confirmation and Unicode nickname length before sending', async ({ page }) => {
  let calls = 0
  page.on('request', request => { if (request.url().includes('/api/auth/')) calls++ })
  await page.goto('/page/auth/register/')
  await page.getByRole('button', { name: '创建账号', exact: true }).click()
  await expect(page.getByText('账号须为 2–24 位字母、数字或下划线')).toBeVisible()
  await page.getByLabel('账号', { exact: true }).fill('Demo_User')
  await page.getByLabel('昵称（可选）').fill('😀'.repeat(25))
  await page.getByLabel('密码', { exact: true }).fill('sample-password')
  await page.getByLabel('确认密码', { exact: true }).fill('different-password')
  await page.getByRole('button', { name: '创建账号', exact: true }).click()
  await expect(page.getByText('两次输入的密码不一致')).toBeVisible()
  await expect(page.getByText('昵称最多 24 个字符，不能包含空字符')).toBeVisible()
  expect(calls).toBe(0)
})

test('wrong credentials can be corrected and password visibility toggled', async ({ page }) => {
  await mockAuth(page)
  await page.goto('/page/auth/login/')
  await page.getByLabel('账号', { exact: true }).fill('Demo_User')
  await page.getByLabel('密码', { exact: true }).fill('wrong-password')
  await page.getByRole('button', { name: '显示密码', exact: true }).click()
  await expect(page.getByLabel('密码', { exact: true })).toHaveAttribute('type', 'text')
  await page.getByRole('button', { name: '隐藏密码', exact: true }).click()
  await expect(page.getByLabel('密码', { exact: true })).toHaveAttribute('type', 'password')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText('账号或密码不正确')
  await page.getByLabel('密码', { exact: true }).fill('sample-password')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page).toHaveURL(/\/auth\/session\/$/)
})

test('duplicate account keeps input and focuses account field', async ({ page }) => {
  await page.route('**/api/auth/register', route => route.fulfill({ status: 409, json: { error: { code: 'username_taken' } } }))
  await page.goto('/page/auth/register/')
  await page.getByLabel('账号', { exact: true }).fill('Demo_User')
  await page.getByLabel('密码', { exact: true }).fill('sample-password')
  await page.getByLabel('确认密码', { exact: true }).fill('sample-password')
  await page.getByRole('button', { name: '创建账号', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText('已被注册')
  await expect(page.getByLabel('账号', { exact: true })).toBeFocused()
  await expect(page.getByLabel('账号', { exact: true })).toHaveValue('Demo_User')
})

test('pending submission is disabled and network failures permit retry', async ({ page }) => {
  await page.route('**/api/auth/login', async route => {
    await new Promise(resolve => setTimeout(resolve, 400))
    await route.abort('failed')
  })
  await page.goto('/page/auth/login/')
  await page.getByLabel('账号', { exact: true }).fill('Demo_User')
  await page.getByLabel('密码', { exact: true }).fill('sample-password')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.getByRole('button', { name: '正在登录…' })).toBeDisabled()
  await expect(page.getByRole('alert')).toContainText('无法连接服务')
  await expect(page.getByRole('button', { name: '登录', exact: true })).toBeEnabled()
})

test('protected route redirects and layouts fit desktop and narrow mobile', async ({ page }, testInfo) => {
  await page.goto('/page/auth/session/')
  await expect(page).toHaveURL(/\/auth\/login\/$/)
  for (const path of ['login', 'register']) {
    await page.goto(`/page/auth/${path}/`)
    await expect(page.getByRole('button', { name: path === 'login' ? '登录' : '创建账号', exact: true })).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
    await page.screenshot({ path: `../../outputs/web-check/${testInfo.project.name}-${path}.png`, fullPage: true })
  }
  await page.setViewportSize({ width: 320, height: 568 })
  await page.goto('/page/auth/register/')
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
})

test('failed logout keeps session, unauthorized logout clears it', async ({ page }) => {
  await mockAuth(page)
  await page.goto('/page/auth/login/')
  await page.getByLabel('账号', { exact: true }).fill('Demo_User')
  await page.getByLabel('密码', { exact: true }).fill('sample-password')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page).toHaveURL(/\/auth\/session\/$/)
  await page.route('**/api/auth/logout', route => route.fulfill({ status: 500, json: { error: { code: 'internal_error' } } }))
  await page.getByRole('button', { name: '退出当前账号' }).click()
  await expect(page.getByRole('alert')).toContainText('服务暂时遇到问题')
  await expect(page.getByRole('heading', { name: '你好，小夏' })).toBeVisible()
  await page.route('**/api/auth/logout', route => route.fulfill({ status: 401, json: { error: { code: 'unauthorized' } } }))
  await page.getByRole('button', { name: '退出当前账号' }).click()
  await expect(page).toHaveURL(/\/auth\/login\/$/)
})
