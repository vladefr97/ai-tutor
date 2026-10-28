// Runs against the backend with AGENT_FAKE=1: the fake Learning Mentor answers
// «Наставник: вижу страницу <pageId>; решено вопросов: <n>» from the context the backend passes.
import { expect, test, type Page } from '@playwright/test'
import { choose, login, planSection, uniqueEmail } from './helpers'

async function openPMCourse(page: Page, email: string) {
  await login(page, email)
  await choose(page, 'Project Manager')
  await page.getByRole('button', { name: /Пропустить интервью/ }).click()
  await expect(planSection(page)).toBeVisible({ timeout: 20_000 })
  await page.getByRole('button', { name: /Подтвердить план/ }).click()
  await expect(page.getByRole('heading', { name: 'Основы управления проектом' })).toBeVisible({ timeout: 20_000 })
}

async function ask(page: Page, text: string) {
  const chat = page.getByRole('complementary', { name: 'Чат с наставником' })
  await chat.getByRole('textbox', { name: 'Сообщение наставнику' }).fill(text)
  await chat.getByRole('button', { name: 'Отправить наставнику' }).click()
  return chat
}

test('наставник видит текущую страницу и свежий прогресс, история переживает reload и повторный вход', async ({ page }) => {
  const email = uniqueEmail('chat')
  await openPMCourse(page, email)
  let chat = await ask(page, 'Что здесь главное?')
  await expect(chat.getByText('Наставник: вижу страницу p-goal; решено вопросов: 0')).toBeVisible({ timeout: 15_000 })

  const q = page.getByRole('form', { name: /описывает результат/ })
  await q.getByLabel('К 1 марта клиенты оформляют заказ онлайн за 3 минуты').check()
  await q.getByRole('button', { name: 'Проверить' }).click()
  await expect(q.getByRole('status')).toContainText('Верно')

  await page.getByRole('button', { name: 'Далее →' }).click()
  chat = await ask(page, 'А тут?')
  await expect(chat.getByText('Наставник: вижу страницу p-risks; решено вопросов: 1')).toBeVisible({ timeout: 15_000 })

  await page.reload()
  await expect(page.getByText('Наставник: вижу страницу p-risks; решено вопросов: 1')).toBeVisible()
  const url = page.url()
  await page.getByRole('button', { name: 'Выйти' }).click()
  await login(page, email)
  await page.goto(url)
  await expect(page.getByText('Что здесь главное?')).toBeVisible()
  await expect(page.getByText('Наставник: вижу страницу p-goal; решено вопросов: 0')).toBeVisible()
})
