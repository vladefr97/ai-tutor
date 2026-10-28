import { expect, test, type Page } from '@playwright/test'
import { readCode } from './mailpit'

const uniqueEmail = () => `learner-${Date.now()}-${Math.random().toString(36).slice(2, 8)}@example.test`

async function requestCode(page: Page, email: string) {
  await page.goto('/')
  await page.getByLabel('Почта').fill(email)
  await page.getByRole('button', { name: 'Получить код' }).click()
  await expect(page.getByLabel('Код из письма')).toBeVisible()
}

test('вход по коду из письма, обновление страницы и выход', async ({ page }) => {
  const email = uniqueEmail()
  await requestCode(page, email)
  await page.getByLabel('Код из письма').fill(await readCode(email))
  await page.getByRole('button', { name: 'Войти' }).click()

  await expect(page.getByRole('heading', { name: 'Чему хотите научиться?' })).toBeVisible()
  for (const name of ['Python Developer', 'Наставник по Git', 'Project Manager']) {
    await expect(page.getByRole('button', { name: new RegExp(name) })).toBeVisible()
  }
  await expect(page.getByText(email)).toBeVisible()
  const noOverflow = () => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)
  expect(await noOverflow()).toBe(true)

  await page.reload()
  await expect(page.getByRole('heading', { name: 'Чему хотите научиться?' })).toBeVisible()

  await page.getByRole('button', { name: 'Выйти' }).click()
  await expect(page.getByLabel('Почта')).toBeVisible()
  await page.reload()
  await expect(page.getByLabel('Почта')).toBeVisible()
  await expect(page.getByText(email)).toHaveCount(0)
})

test('неверный код показывает ошибку и не входит', async ({ page }) => {
  const email = uniqueEmail()
  await requestCode(page, email)
  const real = await readCode(email)
  await page.getByLabel('Код из письма').fill(real === '000000' ? '111111' : '000000')
  await page.getByRole('button', { name: 'Войти' }).click()
  await expect(page.getByRole('alert')).toHaveText('Неверный или истёкший код')
  await page.reload()
  await expect(page.getByLabel('Почта')).toBeVisible()
})
