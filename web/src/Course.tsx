import { useCallback, useEffect, useRef, useState } from 'react'
import { Link, useParams } from 'react-router'
import { api, ApiError, type Schemas } from './api/client'
import { directionIcons } from './icons'

type Course = Schemas['Course']
type Operation = Schemas['Operation']
type Message = Schemas['Message']

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

  const load = useCallback(async () => {
    const [c, m] = await Promise.all([
      api('get', '/courses/{courseId}', undefined, { courseId: id }),
      api('get', '/courses/{courseId}/messages', undefined, { courseId: id }),
    ])
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
      if (active(next)) setOp(next)
      else { await load(); setOp(next) }
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

  if (!course) return null
  const mentor = mentorNames[course.direction]
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
      {course.status === 'interview' ? (
        <Chat short={mentor.short} messages={messages} op={op} notice={notice} onSend={send} onRetry={retry} onSkip={skip} />
      ) : (
        <Stage status={course.status} />
      )}
    </main>
  )
}

function Chat(props: {
  short: string; messages: Message[]; op: Operation | null; notice: string
  onSend: (t: string) => Promise<boolean>; onRetry: () => void; onSkip: () => void
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
          aria-label="Ваш ответ наставнику" placeholder="Расскажите о вашей цели…" rows={2} required
          value={text} onChange={(e) => setText(e.target.value)} disabled={busy}
          onKeyDown={(e) => { if (e.key === 'Enter' && !e.shiftKey) submit(e) }}
        />
        <div className="at-composer-bottom">
          <span>Можно отвечать своими словами</span>
          <button className="at-send" aria-label="Отправить сообщение" disabled={busy || !text.trim()}>↑</button>
        </div>
      </form>
      <button className="at-quiet at-skip" onClick={props.onSkip} disabled={busy}>
        Пропустить интервью и начать с основ →
      </button>
    </>
  )
}

// ponytail: only the planning stage for now; plan review/discussion screens come with #13.
function Stage({ status }: { status: Course['status'] }) {
  return (
    <section className="at-generation">
      <div className="at-spinner" aria-hidden="true" />
      <h1>{statusNames[status]}</h1>
      <p className="at-subtitle">Наставник готовит учебный план по вашей цели. Это займёт пару минут.</p>
    </section>
  )
}
