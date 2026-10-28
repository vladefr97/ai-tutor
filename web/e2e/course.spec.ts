// Runs against the backend with AGENT_FAKE=1: PM generation publishes the course from
// contracts/examples/course/valid/pm-basics (each stage ~0.7 s); the fake reviewer passes a
// short answer only if it contains «заранее», otherwise «Не хватает: зачем готовить меры заранее».
import { expect, test, type Page } from '@playwright/test'
import { choose, login, noOverflow, planSection, uniqueEmail } from './helpers'

async function openPMCourse(page: Page, email: string) {
  await login(page, email)
  await choose(page, 'Project Manager')
  await page.getByRole('button', { name: /Пропустить интервью/ }).click()
  await expect(planSection(page)).toBeVisible({ timeout: 20_000 })
  await page.getByRole('button', { name: /Подтвердить план/ }).click()
  await expect(page.getByRole('heading', { name: 'Готовим курс…' })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Основы управления проектом' })).toBeVisible({ timeout: 20_000 })
}

const question = (page: Page, prompt: RegExp) => page.getByRole('form', { name: prompt })
const lesson = (page: Page) => page.locator('article.at-lesson')

test('теория: порядок блоков, подсказка, переходы и разбор', async ({ page }) => {
  await openPMCourse(page, uniqueEmail('course'))
  await expect(page.getByRole('tab', { name: 'Тест' })).toHaveCount(0)
  const text = await lesson(page).innerText()
  const order = ['Цель проекта описывает', 'Какая формулировка описывает результат', 'Результат должен быть проверяемым'].map((s) => text.indexOf(s))
  expect(order.every((i, n) => i >= 0 && (n === 0 || i > order[n - 1]))).toBe(true)

  const q = question(page, /Какая формулировка/)
  await q.getByLabel('Провести встречи с клиентами').check()
  await q.getByRole('button', { name: 'Проверить' }).click()
  await expect(q.getByText('Подсказка.')).toBeVisible()

  await page.getByRole('button', { name: 'Далее →' }).click()
  await expect(page.getByRole('heading', { name: 'Риски', level: 2 })).toBeVisible()
  await page.getByRole('button', { name: '← Назад' }).click()
  await expect(q.getByLabel('Провести встречи с клиентами')).toBeChecked()
  await expect(q.getByText('Подсказка.')).toBeVisible()

  await q.getByLabel('Начать разработку сайта').check()
  await q.getByRole('button', { name: 'Проверить' }).click()
  await expect(q.getByText('Разбор.')).toBeVisible()
  await page.getByRole('button', { name: 'Далее →' }).click()
  await expect(page.getByRole('heading', { name: 'Риски', level: 2 })).toBeVisible()
})

test('несколько ответов и короткий ответ, восстановление после reload и повторного входа', async ({ page }) => {
  const email = uniqueEmail('course')
  await openPMCourse(page, email)
  await page.getByRole('button', { name: 'Далее →' }).click()

  const multi = question(page, /относится к рискам/)
  await multi.getByLabel('Ключевой разработчик может заболеть').check()
  await multi.getByRole('button', { name: 'Проверить' }).click()
  await expect(multi.getByText('Пока неверно')).toBeVisible()
  await multi.getByLabel('Поставщик может задержать оборудование').check()
  await multi.getByRole('button', { name: 'Проверить' }).click()
  await expect(multi.getByText('Верно', { exact: true })).toBeVisible()

  const short = question(page, /зачем заранее составлять/)
  await short.getByLabel('Ваш ответ').fill('Чтобы знать, что может пойти не так')
  await short.getByRole('button', { name: 'Проверить' }).click()
  await expect(short.getByText('Не хватает: зачем готовить меры заранее')).toBeVisible({ timeout: 15_000 })
  await expect(short.getByText('Подсказка.')).toBeVisible()
  await short.getByLabel('Ваш ответ').fill('Чтобы заранее подготовить меры и снизить последствия')
  await short.getByRole('button', { name: 'Проверить' }).click()
  await expect(short.getByRole('status')).toContainText('Верно', { timeout: 15_000 })

  await page.reload()
  await expect(page.getByRole('heading', { name: 'Риски', level: 2 })).toBeVisible()
  await expect(question(page, /относится к рискам/).getByText('Верно', { exact: true })).toBeVisible()
  await expect(question(page, /зачем заранее составлять/).getByLabel('Ваш ответ')).toHaveValue(/заранее подготовить/)

  const url = page.url()
  await page.getByRole('button', { name: 'Выйти' }).click()
  await login(page, email)
  await page.goto(url)
  await expect(question(page, /относится к рискам/).getByLabel('Поставщик может задержать оборудование')).toBeChecked()
  await expect(question(page, /зачем заранее составлять/).getByRole('status')).toContainText('Верно')
})

test('узкий экран: без переполнения, чат открывается кнопкой', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await openPMCourse(page, uniqueEmail('course'))
  expect(await noOverflow(page)).toBe(true)
  await expect(page.getByText('Чат с наставником скоро появится')).toBeHidden()
  await page.getByRole('button', { name: 'Чат с наставником' }).click()
  await expect(page.getByText('Чат с наставником скоро появится')).toBeVisible()
  expect(await noOverflow(page)).toBe(true)
})
