import { expect, type Page } from '@playwright/test'
import { readCode } from './mailpit'

export const uniqueEmail = (prefix: string) => `${prefix}-${Date.now()}-${Math.random().toString(36).slice(2, 8)}@example.test`

export async function login(page: Page, email: string) {
  await page.goto('/')
  await page.getByLabel('Почта').fill(email)
  await page.getByRole('button', { name: 'Получить код' }).click()
  await page.getByLabel('Код из письма').fill(await readCode(email))
  await page.getByRole('button', { name: 'Войти' }).click()
  await expect(page.getByRole('heading', { name: 'Чему хотите научиться?' })).toBeVisible()
}

export async function choose(page: Page, mentor: string) {
  await page.getByRole('button', { name: new RegExp(mentor) }).click()
  await expect(page.getByText('Какую цель')).toBeVisible({ timeout: 15_000 })
}

export async function answer(page: Page, text: string) {
  await page.getByLabel('Ваш ответ наставнику').fill(text)
  await page.getByRole('button', { name: 'Отправить сообщение' }).click()
}

export const noOverflow = (page: Page) =>
  page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)

export const planSection = (page: Page) => page.getByRole('region', { name: 'План курса' })
