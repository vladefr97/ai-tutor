import { useEffect, useState } from 'react'
import { Link, useParams } from 'react-router'
import { api, ApiError, type Schemas } from './api/client'
import { mentorNames, statusNames } from './Course'
import Learn from './Learn'

type Progress = Schemas['Progress']

const percent = (p: Progress) => (p.questionsTotal ? Math.round((100 * p.questionsPassed) / p.questionsTotal) : 0)

// useTeacherData loads a teacher route; a learner gets the server's 403 as a refusal.
function useTeacherData<T>(load: () => Promise<T>, deps: unknown[]) {
  const [data, setData] = useState<T>()
  const [refused, setRefused] = useState(false)
  useEffect(() => {
    load().then(setData, (e) => {
      if (e instanceof ApiError && e.status === 403) setRefused(true)
      else throw e
    })
  }, deps)
  return { data, refused }
}

const Refused = () => (
  <main className="at-main">
    <h1>Нет доступа</h1>
    <p className="at-subtitle">Кабинет доступен только преподавателю.</p>
  </main>
)

export function Learners() {
  const { data, refused } = useTeacherData(() => api('get', '/teacher/learners'), [])
  if (refused) return <Refused />
  if (!data) return null
  return (
    <main className="at-main">
      <Link className="at-back" to="/">← Наставники</Link>
      <div className="at-eyebrow">Кабинет преподавателя</div>
      <h1>Ученики</h1>
      <p className="at-subtitle">Цели, прогресс и история обучения.</p>
      <div className="at-students">
        {data.length === 0 && <p className="at-muted">Учеников пока нет.</p>}
        {data.map((l) => (
          <section key={l.id} aria-label={l.email}>
            <h2 className="at-small at-muted">{l.email}</h2>
            {l.courses.length === 0 && <p className="at-small at-muted">Курсов нет</p>}
            {l.courses.map((c) => (
              <Link key={c.id} className="at-student" to={`/teacher/courses/${c.id}`}>
                <span>
                  {mentorNames[c.direction].name}
                  <small>{c.goal ?? 'Интервью'}</small>
                </span>
                <small className="at-muted">
                  {statusNames[c.status as Schemas['Course']['status']] ?? c.status} · {percent(c.progress)}% ↗
                </small>
              </Link>
            ))}
          </section>
        ))}
      </div>
      <p className="at-footnote">Только просмотр: действия от имени учеников недоступны.</p>
    </main>
  )
}

type Tab = 'interview' | 'plan' | 'material' | 'attempts' | 'chat'
const tabs: [Tab, string][] = [['interview', 'Интервью'], ['plan', 'План'], ['material', 'Материал'], ['attempts', 'Попытки'], ['chat', 'Чат']]
const attemptStatus: Record<Schemas['Attempt']['status'], string> = { pending: 'проверяется', passed: 'зачтено', failed: 'не зачтено', error: 'ошибка проверки' }

export function LearnerCourse() {
  const { id } = useParams()
  const { data, refused } = useTeacherData(() => api('get', '/teacher/courses/{courseId}', undefined, { courseId: id! }), [id])
  const [tab, setTab] = useState<Tab>('interview')
  if (refused) return <Refused />
  if (!data) return null
  const { learner, course, progress } = data
  const failedOps = data.operations.filter((o) => o.status === 'failed')

  return (
    <main className="at-main at-course">
      <div className="at-readonly">
        Курс ученика: {learner.email} · только просмотр <Link className="at-quiet" to="/teacher">← К списку</Link>
      </div>
      <div className="at-eyebrow">{mentorNames[course.direction].name} · {statusNames[course.status]}</div>
      <h1>{course.goal ?? 'Цель ещё не определена'}</h1>
      <p className="at-small at-muted">
        Вопросов решено: {progress.questionsPassed} из {progress.questionsTotal} · практика: {progress.practicePassed} из {progress.practiceTotal}
      </p>
      {course.profile && (
        <p className="at-small at-muted">
          Подготовка: {course.profile.preparation ?? '—'} · Контекст: {course.profile.context ?? '—'} · Общение: {course.profile.preferences ?? '—'}
        </p>
      )}
      <div className="at-tabs" role="tablist">
        {tabs.map(([t, name]) => (
          <button key={t} role="tab" aria-selected={tab === t} onClick={() => setTab(t)}>{name}</button>
        ))}
      </div>

      {tab === 'interview' && <Messages list={data.messages} empty="Интервью ещё не начиналось." learnerName="Ученик" mentorName="Наставник" />}

      {tab === 'plan' && (
        <section aria-label="Версии плана">
          {data.planVersions.length === 0 && <p className="at-muted">Плана ещё нет.</p>}
          {data.planVersions.map((v) => (
            <details key={v.id} className="at-module" open={v.confirmed}>
              <summary>
                <span className="at-module-title">Версия {v.number}: {v.plan.title}</span>
                <small className="at-muted">{v.confirmed ? 'подтверждена учеником' : v.approved ? 'одобрена ревьюером' : v.review ? 'есть замечания ревьюера' : 'на проверке'}</small>
              </summary>
              <p>{v.explanation}</p>
              <ul>{v.plan.modules.map((m) => <li key={m.title}><b>{m.title}</b> — {m.outcome}</li>)}</ul>
              <p className="at-small">Экзамен: {v.plan.exam}</p>
              {v.review && <pre className="at-small at-muted at-review">{JSON.stringify(v.review, null, 2)}</pre>}
            </details>
          ))}
        </section>
      )}

      {tab === 'material' && (data.content
        ? <Learn course={course} readOnly={{ content: data.content.course, attempts: data.attempts }} />
        : <p className="at-muted">Курс ещё не опубликован.</p>)}

      {tab === 'attempts' && (
        <section aria-label="Попытки">
          {data.attempts.length === 0 && <p className="at-muted">Попыток пока нет.</p>}
          <ul className="at-attempts">
            {data.attempts.map((a) => (
              <li key={a.id}>
                <b>{a.itemId}</b> · {attemptStatus[a.status]} · <small>{new Date(a.createdAt).toLocaleString('ru-RU')}</small>
                <div className="at-small">Ответ: {a.answer.text ?? a.answer.options?.join(', ')}</div>
                {a.feedback && <div className="at-small at-muted">Обратная связь: {a.feedback}</div>}
                {a.hint && <div className="at-small at-muted">Выдана подсказка</div>}
                {a.explanation && <div className="at-small at-muted">Выдан разбор</div>}
              </li>
            ))}
          </ul>
          {failedOps.length > 0 && (
            <>
              <h2 className="at-small">Ошибки подготовки</h2>
              <ul>{failedOps.map((o) => <li key={o.id} className="at-small">{o.kind}: {o.error?.message}</li>)}</ul>
            </>
          )}
        </section>
      )}

      {tab === 'chat' && <Messages list={data.chat} empty="Ученик ещё не писал Учебному наставнику." learnerName="Ученик" mentorName="Наставник" />}
    </main>
  )
}

function Messages({ list, empty, learnerName, mentorName }: { list: Schemas['Message'][]; empty: string; learnerName: string; mentorName: string }) {
  if (list.length === 0) return <p className="at-muted">{empty}</p>
  return (
    <ol className="at-transcript">
      {list.map((m) => (
        <li key={m.id}>
          <b>{m.author === 'learner' ? learnerName : mentorName}:</b> {m.text}
        </li>
      ))}
    </ol>
  )
}
