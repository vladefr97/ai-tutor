import { useCallback, useEffect, useRef, useState } from 'react'
import { Link, useParams } from 'react-router'
import { api, ApiError, type Schemas } from './api/client'
import { directionIcons } from './icons'
import Learn from './Learn'

type Course = Schemas['Course']
type Operation = Schemas['Operation']
type Message = Schemas['Message']
type PlanVersion = Schemas['PlanVersion']

export const mentorNames: Record<Schemas['DirectionId'], { name: string; short: string }> = {
  python: { name: 'Python Developer', short: 'Python' },
  git: { name: 'Наставник по Git', short: 'Git' },
  pm: { name: 'Project Manager', short: 'Project Management' },
}

export const statusNames: Record<Course['status'], string> = {
  interview: 'Интервью',
  planning: 'Составляем план',
  plan_ready: 'План готов',
  awaiting_github: 'Нужно подключить GitHub',
  generating: 'Готовим курс',
  generation_failed: 'Ошибка подготовки',
  ready: 'Курс готов',
  completed: 'Курс завершён',
  exam_failed: 'Экзамен не сдан',
}

const active = (op?: Operation | null) => op?.status === 'queued' || op?.status === 'running'
const POLL_MS = 2000

export default function CoursePage() {
  const id = useParams().id!
  const [course, setCourse] = useState<Course>()
  const [messages, setMessages] = useState<Message[]>([])
  const [op, setOp] = useState<Operation | null>(null)
  const [notice, setNotice] = useState('')
  const [plan, setPlan] = useState<PlanVersion>()

  const load = useCallback(async () => {
    const [c, m] = await Promise.all([
      api('get', '/courses/{courseId}', undefined, { courseId: id }),
      api('get', '/courses/{courseId}/messages', undefined, { courseId: id }),
    ])
    if (c.status === 'plan_ready') setPlan(await api('get', '/courses/{courseId}/plan', undefined, { courseId: id }))
    setCourse(c)
    setMessages(m)
    setOp(c.operation ?? null)
  }, [id])

  useEffect(() => { load() }, [load])

  // Poll the running operation until it is final, then reload course and messages.
  useEffect(() => {
    if (!op || !active(op)) return
    const t = setTimeout(async () => {
      const next = await api('get', '/operations/{operationId}', undefined, { operationId: op.id })
      // A finished operation may be followed by a chained one (interview → planning):
      // the course reports whichever is current now.
      if (active(next)) setOp(next)
      else await load()
    }, POLL_MS)
    return () => clearTimeout(t)
  }, [op, load])

  async function send(text: string) {
    setNotice('')
    try {
      setMessages((m) => [...m, { id: 'local', author: 'learner', text, createdAt: new Date().toISOString() }])
      setOp(await api('post', '/courses/{courseId}/messages', { text }, { courseId: id }))
      return true
    } catch (e) {
      await load()
      if (e instanceof ApiError && e.status === 409) setNotice('Наставник ещё отвечает')
      else throw e
      return false
    }
  }

  async function retry() {
    if (op) setOp(await api('post', '/operations/{operationId}/retry', undefined, { operationId: op.id }))
  }

  async function skip() {
    const c = await api('post', '/courses/{courseId}/interview/skip', undefined, { courseId: id })
    setCourse(c)
    setOp(c.operation ?? null)
  }

  async function confirm() {
    if (!plan) return
    setNotice('')
    try {
      const c = await api('post', '/courses/{courseId}/plan/confirm', { planVersionId: plan.id }, { courseId: id })
      setCourse(c)
      setOp(c.operation ?? null)
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 409)) throw e
      await load()
      setNotice('План обновился — проверьте новую версию')
    }
  }

  if (!course) return null
  const mentor = mentorNames[course.direction]
  if (course.status === 'ready' || course.status === 'completed' || course.status === 'exam_failed') {
    return <main className="at-main at-course"><Learn course={course} /></main>
  }
  const chat = (placeholder: string, onSkip?: () => void) => (
    <Chat short={mentor.short} messages={messages} op={op} notice={notice} placeholder={placeholder}
      onSend={send} onRetry={retry} onSkip={onSkip} />
  )
  return (
    <main className="at-main">
      <Link className="at-back" to="/">← Все наставники</Link>
      <div className="at-context">
        <span className="at-icon">{directionIcons[course.direction]}</span>
        <div>
          <h1>{mentor.name}</h1>
          <small>Ваш наставник · подготовка курса</small>
        </div>
      </div>
      {course.status === 'interview' && chat('Расскажите о вашей цели…', skip)}
      {course.status === 'planning' && (
        <Stages op={op} onRetry={retry} stages={planStages} title="Готовим ваш план"
          text="Наставник составляет учебный план по вашей цели. Это займёт пару минут." />
      )}
      {course.status === 'plan_ready' && plan && (
        <>
          <Plan version={plan} busy={active(op)} onConfirm={confirm} />
          {chat('Что изменить в плане? Или задайте вопрос…')}
        </>
      )}
      {(course.status === 'generating' || course.status === 'generation_failed') && (
        <Stages op={op} onRetry={retry} stages={courseStages} title="Готовим курс…"
          text="Собираем материалы по утверждённому плану. Это займёт несколько минут." />
      )}
      {course.status === 'awaiting_github' && (
        <section className="at-generation">
          <h1>Подключите GitHub</h1>
          <p className="at-subtitle">
            Практика по {mentor.short} ведётся в вашем учебном репозитории на GitHub. Подключение аккаунта появится
            следующим шагом — после него мы подготовим курс и репозиторий.
          </p>
        </section>
      )}
      {!['interview', 'planning', 'plan_ready', 'generating', 'generation_failed', 'awaiting_github'].includes(course.status) && (
        <Waiting title={statusNames[course.status]} text="" />
      )}
    </main>
  )
}

function Chat(props: {
  short: string; messages: Message[]; op: Operation | null; notice: string; placeholder: string
  onSend: (t: string) => Promise<boolean>; onRetry: () => void; onSkip?: () => void
}) {
  const { short, messages, op, notice } = props
  const [text, setText] = useState('')
  const end = useRef<HTMLDivElement>(null)
  const busy = active(op)
  useEffect(() => { end.current?.scrollIntoView?.({ block: 'nearest' }) }, [messages.length, busy])

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    const t = text.trim()
    if (!t || busy) return
    setText('')
    if (!(await props.onSend(t))) setText(t)
  }

  return (
    <>
      {messages.map((m) => (
        <div key={m.id + m.createdAt} className={'at-message' + (m.author === 'learner' ? ' at-user' : '')}>
          {m.author === 'mentor' && <div className="at-message-label">{short} · наставник</div>}
          <p>{m.text}</p>
        </div>
      ))}
      {busy && <p className="at-typing" role="status">Наставник печатает…</p>}
      {op?.status === 'failed' && (
        <div className="at-op-error" role="alert">
          <span>{op.error?.message ?? 'Наставник не ответил'}</span>
          <button className="at-secondary" onClick={props.onRetry}>Повторить</button>
        </div>
      )}
      {notice && <p className="at-notice" role="status">{notice}</p>}
      <div ref={end} />
      <form className="at-composer" onSubmit={submit}>
        <textarea
          aria-label="Ваш ответ наставнику" placeholder={props.placeholder} rows={2} required
          value={text} onChange={(e) => setText(e.target.value)} disabled={busy}
          onKeyDown={(e) => { if (e.key === 'Enter' && !e.shiftKey) submit(e) }}
        />
        <div className="at-composer-bottom">
          <span>Можно отвечать своими словами</span>
          <button className="at-send" aria-label="Отправить сообщение" disabled={busy || !text.trim()}>↑</button>
        </div>
      </form>
      {props.onSkip && (
        <button className="at-quiet at-skip" onClick={props.onSkip} disabled={busy}>
          Пропустить интервью и начать с основ →
        </button>
      )}
    </>
  )
}

type Stage = { id: NonNullable<Operation['stage']>; name: string }
const planStages: Stage[] = [
  { id: 'drafting_plan', name: 'Составляем план' },
  { id: 'reviewing_plan', name: 'Проверяем план' },
  { id: 'revising_plan', name: 'Вносим исправления' },
]
const courseStages: Stage[] = [
  { id: 'generating', name: 'Пишем материалы' },
  { id: 'reviewing_content', name: 'Проверяем задания' },
  { id: 'uploading', name: 'Сохраняем курс' },
]

function Stages({ op, onRetry, stages, title, text }: {
  op: Operation | null; onRetry: () => void; stages: Stage[]; title: string; text: string
}) {
  const current = Math.max(0, stages.findIndex((s) => s.id === op?.stage))
  return (
    <section className="at-generation">
      {op?.status !== 'failed' && <div className="at-spinner" aria-hidden="true" />}
      <h1>{title}</h1>
      <p className="at-subtitle">{text}</p>
      <ol>
        {stages.map((s, i) => (
          <li key={s.id} className={i < current ? 'at-done' : i === current ? 'at-current' : ''} aria-current={i === current ? 'step' : undefined}>
            <span>{i < current ? '✓' : i === current ? '◌' : '·'}</span>
            {s.name}
          </li>
        ))}
      </ol>
      {op?.status === 'failed' && (
        <div className="at-op-error" role="alert">
          <span>{op.error?.message ?? 'Не получилось'}</span>
          <button className="at-secondary" onClick={onRetry}>Повторить</button>
        </div>
      )}
    </section>
  )
}

function Plan({ version, busy, onConfirm }: { version: PlanVersion; busy: boolean; onConfirm: () => void }) {
  const { plan } = version
  return (
    <>
      <div className="at-message">
        <div className="at-message-label">Наставник</div>
        <p>{version.explanation}</p>
      </div>
      <section className="at-plan" aria-label="План курса">
        <div className="at-eyebrow">План курса</div>
        <h2 className="at-plan-title">{plan.title}</h2>
        <p className="at-muted at-small">Ваша цель: {plan.goal}</p>
        <div className="at-meta">
          <span>Модулей: {plan.modules.length}</span>
          <span>{plan.hasPractice ? 'С практикой' : 'Без практики'}</span>
          <span>Итоговый экзамен</span>
        </div>
        <div className="at-toc">
          {plan.modules.map((m, i) => (
            <details key={i} className="at-module">
              <summary>
                <span className="at-number">{String(i + 1).padStart(2, '0')}</span>
                <span className="at-module-title">{m.title}</span>
                <span className="at-chevron" aria-hidden="true">›</span>
              </summary>
              <div className="at-module-body">
                <ul>{m.topics.map((t) => <li key={t}>{t}</li>)}</ul>
                {m.practice.length > 0 && (
                  <p><b>Практика</b><br />{m.practice.join('; ')}</p>
                )}
                <p><b>Результат</b><br />{m.outcome}</p>
              </div>
            </details>
          ))}
        </div>
        <p className="at-small at-muted at-exam"><b>Итоговый экзамен.</b> {plan.exam}</p>
        <div className="at-actions">
          <button className="at-primary" onClick={onConfirm} disabled={busy}>Подтвердить план и создать курс →</button>
        </div>
      </section>
    </>
  )
}

function Waiting({ title, text }: { title: string; text: string }) {
  return (
    <section className="at-generation">
      <div className="at-spinner" aria-hidden="true" />
      <h1>{title}</h1>
      {text && <p className="at-subtitle">{text}</p>}
    </section>
  )
}
