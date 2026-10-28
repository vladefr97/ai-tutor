// Runs against the backend started with AGENT_FAKE=1. Fake planning: every planner/reviewer
// turn takes ~0.7 s; the first review of a course asks for plan fixes, the second approves.
// The plan has 3 modules; «добавь …» in the discussion yields an updated plan with 4 modules
// (approved at once); any other message gets «Отвечаю на вопрос» and no new version.
import { expect, test } from '@playwright/test'
import { answer, choose, login, noOverflow, planSection, uniqueEmail } from './helpers'

test('PM: этапы, план, обсуждение, новая версия и подтверждение', async ({ page }) => {
  await login(page, uniqueEmail('planning'))
  await choose(page, 'Project Manager')
  await answer(page, 'Хочу планировать проекты')
  await expect(page.getByText('Что ты уже умеешь')).toBeVisible({ timeout: 15_000 })
  await answer(page, 'Начинаю с нуля')

  await expect(page.getByRole('heading', { name: 'Готовим ваш план' })).toBeVisible({ timeout: 15_000 })
  await expect(page.locator('li[aria-current="step"]', { hasText: 'Вносим исправления' })).toBeVisible({ timeout: 15_000 })
  await expect(page.getByText('Ревьюер')).toHaveCount(0)

  const plan = planSection(page)
  await expect(plan).toBeVisible({ timeout: 20_000 })
  await expect(plan.locator('.at-module')).toHaveCount(3)
  await expect(page.getByText('Объясняю, почему план такой')).toBeVisible()
  await plan.getByText('Модуль 1. Основы', { exact: true }).click()
  await expect(plan.locator('.at-module[open] li').first()).toBeVisible()

  await answer(page, 'Сколько времени займёт курс?')
  await expect(page.getByText('Отвечаю на вопрос')).toBeVisible({ timeout: 15_000 })
  await expect(plan.locator('.at-module')).toHaveCount(3)

  await answer(page, 'добавь модуль про бюджет')
  await expect(page.getByText('Хорошо, обновлю план.')).toBeVisible({ timeout: 15_000 })
  await expect(planSection(page).getByText('План (обновлён)')).toBeVisible({ timeout: 20_000 })
  await expect(planSection(page).locator('.at-module')).toHaveCount(4)
  await expect(planSection(page).getByText('Модуль 4. Дополнительно', { exact: true })).toBeVisible()

  await page.getByRole('button', { name: /Подтвердить план/ }).click()
  await expect(page.getByRole('heading', { name: 'Готовим курс…' })).toBeVisible()
  await page.reload()
  await expect(page.getByRole('heading', { name: 'Готовим курс…' })).toBeVisible()
})

test('Python: пропуск интервью, план и шаг подключения GitHub', async ({ page }) => {
  await login(page, uniqueEmail('planning'))
  await choose(page, 'Python Developer')
  await page.getByRole('button', { name: /Пропустить интервью/ }).click()
  await expect(planSection(page)).toBeVisible({ timeout: 20_000 })
  await page.getByRole('button', { name: /Подтвердить план/ }).click()
  await expect(page.getByRole('heading', { name: 'Подключите GitHub' })).toBeVisible()
})

test('план без горизонтального переполнения на 390 px', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await login(page, uniqueEmail('planning'))
  await choose(page, 'Наставник по Git')
  await page.getByRole('button', { name: /Пропустить интервью/ }).click()
  await expect(planSection(page)).toBeVisible({ timeout: 20_000 })
  await planSection(page).locator('summary').first().click()
  expect(await noOverflow(page)).toBe(true)
})
