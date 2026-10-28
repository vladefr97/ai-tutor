// Runs against the backend started with AGENT_FAKE=1: the fake planner greets with
// «Какую цель», answers the first learner message with «Что ты уже умеешь», the second
// with «Составляю план» (course → planning), and fails once on a message containing «сбой».
import { expect, test, type Page } from '@playwright/test'
import { readCode } from './mailpit'

const uniqueEmail = () => `interview-${Date.now()}-${Math.random().toString(36).slice(2, 8)}@example.test`

async function login(page: Page, email: string) {
  await page.goto('/')
  await page.getByLabel('Почта').fill(email)
  await page.getByRole('button', { name: 'Получить код' }).click()
  await page.getByLabel('Код из письма').fill(await readCode(email))
  await page.getByRole('button', { name: 'Войти' }).click()
  await expect(page.getByRole('heading', { name: 'Чему хотите научиться?' })).toBeVisible()
}

async function choose(page: Page, mentor: string) {
  await page.getByRole('button', { name: new RegExp(mentor) }).click()
  await expect(page.getByText('Какую цель')).toBeVisible({ timeout: 15_000 })
}

async function answer(page: Page, text: string) {
  await page.getByLabel('Ваш ответ наставнику').fill(text)
  await page.getByRole('button', { name: 'Отправить сообщение' }).click()
}

const noOverflow = (page: Page) =>
  page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)

test('интервью с Project Manager до этапа плана, история переживает reload и повторный вход', async ({ page }) => {
  const email = uniqueEmail()
  await login(page, email)
  await choose(page, 'Project Manager')
  expect(await noOverflow(page)).toBe(true)

  await answer(page, 'Хочу научиться планировать небольшие проекты')
  await expect(page.getByText('Что ты уже умеешь')).toBeVisible({ timeout: 15_000 })
  await page.reload()
  await expect(page.getByText('Хочу научиться планировать небольшие проекты')).toBeVisible()
  await expect(page.getByText('Что ты уже умеешь')).toBeVisible()

  await answer(page, 'Пока ничего, начинаю с нуля')
  await expect(page.getByRole('heading', { name: 'Составляем план' })).toBeVisible({ timeout: 15_000 })
  expect(await noOverflow(page)).toBe(true)

  await page.getByRole('button', { name: 'Выйти' }).click()
  await login(page, email)
  await page.getByRole('link', { name: /Project Manager/ }).click()
  await expect(page.getByRole('heading', { name: 'Составляем план' })).toBeVisible()
})

test('пропуск интервью ведёт к составлению плана', async ({ page }) => {
  await login(page, uniqueEmail())
  await choose(page, 'Python Developer')
  await page.getByRole('button', { name: /Пропустить интервью/ }).click()
  await expect(page.getByRole('heading', { name: 'Составляем план' })).toBeVisible()
})

test('сбой ответа показывает ошибку, «Повторить» даёт ответ', async ({ page }) => {
  await login(page, uniqueEmail())
  await choose(page, 'Наставник по Git')
  await answer(page, 'Проверим сбой')
  await expect(page.getByRole('alert')).toContainText('Наставник не ответил', { timeout: 15_000 })
  await page.getByRole('button', { name: 'Повторить' }).click()
  await expect(page.getByText('Что ты уже умеешь')).toBeVisible({ timeout: 15_000 })
  await expect(page.getByRole('alert')).toHaveCount(0)
})
