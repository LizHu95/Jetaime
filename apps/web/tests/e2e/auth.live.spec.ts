import { expect, test } from '@playwright/test'

// Run only against a separately started API with an isolated test schema.
test.skip(!process.env.WEB_LIVE_AUTH, 'Set WEB_LIVE_AUTH=1 for real backend acceptance')

test('real Go API: registration, duplicate account, wrong password, login and logout', async ({ page }) => {
  const username = `web_${crypto.randomUUID().replaceAll('-', '').slice(0, 16)}`
  const password = ' 测试密码😀123 '
  await page.goto('/page/auth/register/')
  await page.getByLabel('账号', { exact: true }).fill(username)
  await page.getByLabel('昵称（可选）').fill('前端联调')
  await page.getByLabel('密码', { exact: true }).fill(password)
  await page.getByLabel('确认密码', { exact: true }).fill(password)
  await page.getByRole('button', { name: '创建账号', exact: true }).click()
  await expect(page.getByRole('status')).toContainText('注册成功')

  await page.goto('/page/auth/register/')
  await page.getByLabel('账号', { exact: true }).fill(username)
  await page.getByLabel('密码', { exact: true }).fill(password)
  await page.getByLabel('确认密码', { exact: true }).fill(password)
  await page.getByRole('button', { name: '创建账号', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText('已被注册')

  await page.goto('/page/auth/login/')
  await page.getByLabel('账号', { exact: true }).fill(username)
  await page.getByLabel('密码', { exact: true }).fill('wrong-password')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText('账号或密码不正确')
  await page.getByLabel('密码', { exact: true }).fill(password)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.getByRole('heading', { name: '你好，前端联调' })).toBeVisible()
  await page.reload()
  await expect(page.getByRole('heading', { name: '你好，前端联调' })).toBeVisible()
  await page.getByRole('button', { name: '退出当前账号' }).click()
  await expect(page).toHaveURL(/\/auth\/login\/$/)
})
