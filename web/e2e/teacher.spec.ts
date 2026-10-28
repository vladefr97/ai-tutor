// Runs against `AGENT_FAKE=1 make up`; the teacher is ADMIN_EMAIL (teacher@example.com by default).
import { expect, test } from '@playwright/test'
import { choose, login, noOverflow, planSection, uniqueEmail } from './helpers'

const TEACHER = process.env.TEACHER_EMAIL ?? 'teacher@example.com'

test('преподаватель видит курс ученика только для просмотра', async ({ browser }) => {
  const learner = await browser.newPage()
  const email = uniqueEmail('learner')
  await login(learner, email)
  await expect(learner.getByRole('link', { name: 'Кабинет преподавателя' })).toHaveCount(0)
  await choose(learner, 'Project Manager')
  await learner.getByRole('button', { name: /Пропустить интервью/ }).click()
  await expect(planSection(learner)).toBeVisible({ timeout: 20_000 })
  await learner.getByRole('button', { name: /Подтвердить план/ }).click()
  await expect(learner.getByRole('heading', { name: 'Основы управления проектом' })).toBeVisible({ timeout: 20_000 })
  const q = learner.getByRole('form', { name: /описывает результат/ })
  await q.getByLabel('Провести встречи с клиентами').check()
  await q.getByRole('button', { name: 'Проверить' }).click()
  await expect(q.getByText('Пока неверно')).toBeVisible()

  // A learner opening the cabinet directly is refused by the server.
  await learner.goto('/teacher')
  await expect(learner.getByRole('heading', { name: 'Нет доступа' })).toBeVisible()

  const teacher = await (await browser.newContext()).newPage()
  await login(teacher, TEACHER)
  await teacher.getByRole('link', { name: 'Кабинет преподавателя' }).click()
  await expect(teacher.getByRole('heading', { name: 'Ученики' })).toBeVisible()
  await teacher.getByRole('region', { name: email }).getByRole('link').click()
  await expect(teacher.getByText(`Курс ученика: ${email}`)).toBeVisible()
  await expect(teacher.getByText(/Вопросов решено: 0 из 3/)).toBeVisible()

  await teacher.getByRole('tab', { name: 'Интервью' }).click()
  await expect(teacher.getByText(/Какую цель/)).toBeVisible()
  await teacher.getByRole('tab', { name: 'План' }).click()
  await expect(teacher.getByText('подтверждена учеником')).toBeVisible()

  await teacher.getByRole('tab', { name: 'Материал' }).click()
  const view = teacher.getByRole('form', { name: /описывает результат/ })
  await expect(view.getByLabel('Провести встречи с клиентами')).toBeChecked()
  await expect(view.getByLabel('Провести встречи с клиентами')).toBeDisabled()
  await expect(view.getByRole('button', { name: 'Проверить' })).toBeDisabled()
  await expect(view.getByText('Пока неверно')).toBeVisible()

  await teacher.getByRole('tab', { name: 'Попытки' }).click()
  await expect(teacher.getByText(/q-goal-single · не зачтено/)).toBeVisible()
  await teacher.getByRole('tab', { name: 'Чат' }).click()
  await expect(teacher.getByText('Ученик ещё не писал Учебному наставнику.')).toBeVisible()
  expect(await noOverflow(teacher)).toBe(true)
})
