// Runs against the backend started with AGENT_FAKE=1: the fake planner greets with
// «Какую цель», answers the first learner message with «Что ты уже умеешь», the second
// with «Составляю план» (course → planning → plan), and fails once on a message containing «сбой».
import { expect, test } from '@playwright/test'
import { answer, choose, login, noOverflow, planSection, uniqueEmail as email } from './helpers'

const uniqueEmail = () => email('interview')

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
  await expect(planSection(page)).toBeVisible({ timeout: 20_000 })
  expect(await noOverflow(page)).toBe(true)

  await page.getByRole('button', { name: 'Выйти' }).click()
  await login(page, email)
  await page.getByRole('link', { name: /Project Manager/ }).click()
  await expect(planSection(page)).toBeVisible({ timeout: 20_000 })
})

test('пропуск интервью ведёт к составлению плана', async ({ page }) => {
  await login(page, uniqueEmail())
  await choose(page, 'Python Developer')
  await page.getByRole('button', { name: /Пропустить интервью/ }).click()
  await expect(planSection(page)).toBeVisible({ timeout: 20_000 })
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
