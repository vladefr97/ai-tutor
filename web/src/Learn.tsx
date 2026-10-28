import { useCallback, useEffect, useMemo, useState } from 'react'
import Markdown from 'react-markdown'
import { useSearchParams } from 'react-router'
import { api, type Schemas } from './api/client'

// Public course version as in contracts/course.schema.json (CourseContent.course is untyped in OpenAPI).
type Option = { id: string; text: string }
type Block =
  | { id: string; type: 'markdown'; markdown: string }
  | { id: string; type: 'single_choice' | 'multiple_choice' | 'short_answer'; prompt: string; options?: Option[] }
type Question = Exclude<Block, { type: 'markdown' }>
type Page = { id: string; title: string; blocks: Block[] }
type PublicCourse = { title: string; goal: string; modules: { id: string; title: string; pages: Page[] }[] }
type Attempt = Schemas['Attempt']

const POLL_MS = 2000

export default function Learn({ course }: { course: Schemas['Course'] }) {
  const [content, setContent] = useState<PublicCourse>()
  // Questions initialise their inputs from saved attempts, so render only when both are loaded.
  const [attempts, setAttempts] = useState<Attempt[]>()
  const [tab, setTab] = useState<'theory' | 'practice' | 'exam'>('theory')
  const [chatOpen, setChatOpen] = useState(false)
  const [params, setParams] = useSearchParams()

  useEffect(() => {
    api('get', '/courses/{courseId}/content', undefined, { courseId: course.id }).then((c) => setContent(c.course as unknown as PublicCourse))
    api('get', '/courses/{courseId}/attempts', undefined, { courseId: course.id }).then(setAttempts)
  }, [course.id])

  const pages = useMemo(() => content?.modules.flatMap((m) => m.pages) ?? [], [content])
  const questions = useMemo(() => pages.flatMap((p) => p.blocks.filter((b): b is Question => b.type !== 'markdown')), [pages])
  const record = useCallback((a: Attempt) => setAttempts((list = []) => [...list.filter((x) => x.id !== a.id), a]), [])
  if (!content || !attempts) return null

  const index = Math.max(0, pages.findIndex((p) => p.id === params.get('page')))
  const page = pages[index]
  const go = (i: number) => { setParams({ page: pages[i].id }); window.scrollTo?.(0, 0) }
  const passed = new Set(attempts.filter((a) => a.status === 'passed').map((a) => a.itemId))
  const done = questions.filter((q) => passed.has(q.id)).length

  return (
    <div className={'at-course-layout' + (chatOpen ? ' at-chat-open' : '')}>
      <div className="at-course-content">
        <div className="at-course-top">
          <div>
            <div className="at-eyebrow">Курс</div>
            <h1>{content.title}</h1>
            <p className="at-muted at-small">Цель: {content.goal}</p>
          </div>
          <button className="at-secondary at-chat-launch" onClick={() => setChatOpen(true)}>Чат с наставником</button>
        </div>
        <div className="at-progress" aria-hidden="true"><span style={{ width: `${questions.length ? (100 * done) / questions.length : 0}%` }} /></div>
        <p className="at-small at-muted">Вопросов решено: {done} из {questions.length}</p>

        <div className="at-tabs" role="tablist">
          {([['theory', 'Материал'], ['practice', 'Практика'], ['exam', 'Экзамен']] as const).map(([id, name]) => (
            <button key={id} role="tab" aria-selected={tab === id} onClick={() => setTab(id)}>{name}</button>
          ))}
        </div>

        {tab !== 'theory' ? (
          <p className="at-muted">Скоро: {tab === 'practice' ? 'практические задания появятся здесь' : 'итоговый экзамен появится здесь'}.</p>
        ) : (
          <>
            <details className="at-course-toc">
              <summary>Оглавление</summary>
              {content.modules.map((m) => (
                <div key={m.id}>
                  <b>{m.title}</b>
                  <ul>
                    {m.pages.map((p) => (
                      <li key={p.id}>
                        <button aria-current={p.id === page.id ? 'page' : undefined} onClick={() => go(pages.indexOf(p))}>{p.title}</button>
                      </li>
                    ))}
                  </ul>
                </div>
              ))}
            </details>
            <nav className="at-page-steps" aria-label="Страницы">
              {pages.map((p, i) => (
                <button key={p.id} className={i === index ? 'current' : ''} onClick={() => go(i)}>
                  <span>{i + 1}</span>{p.title}
                </button>
              ))}
            </nav>
            <article className="at-lesson" aria-label={page.title}>
              <h2>{page.title}</h2>
              {page.blocks.map((b) =>
                b.type === 'markdown' ? (
                  <div key={b.id} className="at-md"><Markdown>{b.markdown}</Markdown></div>
                ) : (
                  <QuestionBlock key={b.id} courseId={course.id} q={b}
                    attempts={attempts.filter((a) => a.itemId === b.id)} onAttempt={record} />
                ),
              )}
            </article>
            <div className="at-page-nav">
              <button className="at-secondary" disabled={index === 0} onClick={() => go(index - 1)}>← Назад</button>
              <span className="at-small at-muted">{index + 1} / {pages.length}</span>
              <button className="at-primary" disabled={index === pages.length - 1} onClick={() => go(index + 1)}>Далее →</button>
            </div>
          </>
        )}
      </div>
      {chatOpen && <div className="at-chat-shade" onClick={() => setChatOpen(false)} />}
      <aside className="at-chat-panel" aria-label="Чат с наставником">
        <div className="at-chat-head">
          <h2>Наставник</h2>
          <button className="at-quiet at-chat-close" onClick={() => setChatOpen(false)}>Закрыть</button>
        </div>
        <p className="at-chat-lock">Чат с наставником скоро появится.</p>
      </aside>
    </div>
  )
}

function QuestionBlock({ courseId, q, attempts, onAttempt }: {
  courseId: string; q: Question; attempts: Attempt[]; onAttempt: (a: Attempt) => void
}) {
  const sorted = [...attempts].sort((a, b) => a.createdAt.localeCompare(b.createdAt))
  const last = sorted.at(-1)
  const hint = [...sorted].reverse().find((a) => a.hint)?.hint
  const explanation = [...sorted].reverse().find((a) => a.explanation)?.explanation
  const [options, setOptions] = useState<string[]>(last?.answer.options ?? [])
  const [text, setText] = useState(last?.answer.text ?? '')
  const pending = last?.status === 'pending'

  // Short answers are graded asynchronously: poll until the attempt is final.
  useEffect(() => {
    if (!last || last.status !== 'pending') return
    const t = setTimeout(async () => onAttempt(await api('get', '/attempts/{attemptId}', undefined, { attemptId: last.id })), POLL_MS)
    return () => clearTimeout(t)
  }, [last, onAttempt])

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    const answer = q.type === 'short_answer' ? { text: text.trim() } : { options }
    onAttempt(await api('post', '/courses/{courseId}/attempts', { itemId: q.id, answer }, { courseId }))
  }

  const multiple = q.type === 'multiple_choice'
  const empty = q.type === 'short_answer' ? !text.trim() : options.length === 0
  return (
    <form className="at-quiz at-inline-quiz" onSubmit={submit} aria-label={'Вопрос: ' + q.prompt}>
      <div className="at-quiz-top"><span>{multiple ? 'Выберите все верные ответы' : q.type === 'single_choice' ? 'Выберите один ответ' : 'Ответьте своими словами'}</span></div>
      <p className="at-question">{q.prompt}</p>
      {q.type === 'short_answer' ? (
        <textarea className="at-answer" aria-label="Ваш ответ" value={text} onChange={(e) => setText(e.target.value)} rows={4} />
      ) : (
        q.options?.map((o) => (
          <label key={o.id} className="at-option">
            <input
              type={multiple ? 'checkbox' : 'radio'} name={q.id} value={o.id} checked={options.includes(o.id)}
              onChange={(e) => setOptions(multiple ? (e.target.checked ? [...options, o.id] : options.filter((x) => x !== o.id)) : [o.id])}
            />
            <span className="at-option-text">{o.text}</span>
          </label>
        ))
      )}
      {last && !pending && (
        <div role="status" className={'at-feedback' + (last.status === 'passed' ? '' : ' is-wrong')}>
          {last.status === 'passed' ? 'Верно' : last.status === 'error' ? 'Не удалось проверить ответ. Отправьте ещё раз.' : 'Пока неверно'}
          {last.feedback && <p>{last.feedback}</p>}
        </div>
      )}
      {hint && last?.status !== 'passed' && <div className="at-feedback is-hint"><b>Подсказка.</b> {hint}</div>}
      {explanation && <div className="at-feedback is-hint"><b>Разбор.</b> {explanation}</div>}
      <div className="at-quiz-actions">
        {pending ? <span className="at-small at-muted" role="status">Проверяем ответ…</span> : <span />}
        <button className="at-secondary" disabled={pending || empty || last?.status === 'passed'}>Проверить</button>
      </div>
    </form>
  )
}
