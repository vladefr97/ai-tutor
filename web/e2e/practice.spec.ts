// Runs against the backend with AGENT_FAKE=1: the PM course is pm-basics with the case
// «План переезда офиса»; the fake Work reviewer passes a case that mentions «риск» and is
// longer than 40 characters, otherwise «Не хватает: риски и меры реагирования».
import { expect, test, type Page } from '@playwright/test'
import { choose, login, noOverflow, planSection, uniqueEmail } from './helpers'

async function openPractice(page: Page, email: string) {
  await login(page, email)
  await choose(page, 'Project Manager')
  await page.getByRole('button', { name: /Пропустить интервью/ }).click()
  await expect(planSection(page)).toBeVisible({ timeout: 20_000 })
  await page.getByRole('button', { name: /Подтвердить план/ }).click()
  await expect(page.getByRole('heading', { name: 'Основы управления проектом' })).toBeVisible({ timeout: 20_000 })
  await page.getByRole('tab', { name: 'Практика' }).click()
}

const caseForm = (page: Page) => page.getByRole('form', { name: /План переезда офиса/ })

test('кейс PM: замечания, исправление, зачёт и история после повторного входа', async ({ page }) => {
  const email = uniqueEmail('practice')
  await openPractice(page, email)
  await expect(page.getByRole('heading', { name: 'План переезда офиса' })).toBeVisible()
  await expect(page.getByText('переезжает в новый офис через 6 недель')).toBeVisible()

  const form = caseForm(page)
  await form.getByLabel('Ваше решение').fill('Цель: переехать. Задачи: упаковать и перевезти.')
  await form.getByRole('button', { name: /Отправить на ревью/ }).click()
  await expect(form.getByRole('status')).toContainText('Не хватает: риски и меры реагирования', { timeout: 15_000 })
  await expect(form.getByText('Подсказка.')).toHaveCount(0)

  await form.getByLabel('Ваше решение').fill('Цель со сроком, задачи по неделям и три риска с мерами реагирования.')
  await form.getByRole('button', { name: /Отправить на ревью/ }).click()
  await expect(form.getByRole('status')).toContainText('Работа зачтена', { timeout: 15_000 })

  await page.reload()
  await expect(caseForm(page).getByRole('status')).toContainText('Работа зачтена')
  await page.getByRole('button', { name: 'Выйти' }).click()
  await login(page, email)
  await page.getByRole('link', { name: /Project Manager/ }).click()
  await page.getByRole('tab', { name: 'Практика' }).click()
  await caseForm(page).getByText(/История попыток/).click()
  await expect(caseForm(page).getByText('Не хватает: риски и меры реагирования')).toBeVisible()
})

test('связанная страница открывает материал, узкий экран без переполнения', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await openPractice(page, uniqueEmail('practice'))
  expect(await noOverflow(page)).toBe(true)
  await page.getByRole('button', { name: 'Риски' }).click()
  await expect(page.getByRole('heading', { name: 'Риски', level: 2 })).toBeVisible()
})
