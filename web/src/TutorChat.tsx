import { useCallback, useEffect, useRef, useState } from 'react'
import { ApiError, api, type Schemas } from './api/client'

type Message = Schemas['Message']
type Operation = Schemas['Operation']

const POLL_MS = 2000
const active = (op: Operation | null) => op?.status === 'queued' || op?.status === 'running'

// Chat with the Learning Mentor on the course page; pageId tells the mentor where the learner is.
export default function TutorChat({ courseId, pageId }: { courseId: string; pageId: string }) {
  const [messages, setMessages] = useState<Message[]>([])
  const [op, setOp] = useState<Operation | null>(null)
  const [text, setText] = useState('')
  const [notice, setNotice] = useState('')
  const end = useRef<HTMLDivElement>(null)
  const busy = active(op)

  const load = useCallback(async () => {
    const [history, course] = await Promise.all([
      api('get', '/courses/{courseId}/chat', undefined, { courseId }),
      api('get', '/courses/{courseId}', undefined, { courseId }),
    ])
    setMessages(history)
    setOp(course.operation?.kind === 'tutor_chat' ? course.operation : null)
  }, [courseId])

  useEffect(() => { load() }, [load])
  useEffect(() => { end.current?.scrollIntoView?.({ block: 'nearest' }) }, [messages.length, busy])

  // Poll the running reply until it is final, then reload the history.
  useEffect(() => {
    if (!op || !active(op)) return
    const t = setTimeout(async () => {
      const next = await api('get', '/operations/{operationId}', undefined, { operationId: op.id })
      if (active(next)) setOp(next)
      else {
        await load()
        setOp(next.status === 'failed' ? next : null)
      }
    }, POLL_MS)
    return () => clearTimeout(t)
  }, [op, load])

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    const t = text.trim()
    if (!t || busy) return
    setNotice('')
    try {
      const started = await api('post', '/courses/{courseId}/chat', { text: t, pageId }, { courseId })
      setText('')
      setMessages((m) => [...m, { id: started.id, author: 'learner', text: t, createdAt: new Date().toISOString() }])
      setOp(started)
    } catch (err) {
      setNotice(err instanceof ApiError && err.status === 409 ? 'Наставник ещё отвечает' : 'Не удалось отправить сообщение')
    }
  }

  async function retry() {
    if (op) setOp(await api('post', '/operations/{operationId}/retry', undefined, { operationId: op.id }))
  }

  return (
    <div className="at-tutor">
      <div className="at-tutor-log">
        {messages.length === 0 && <p className="at-chat-lock">Спросите наставника о материале этой страницы или курса.</p>}
        {messages.map((m) => (
          <div key={m.id + m.author} className={'at-message' + (m.author === 'learner' ? ' at-user' : '')}>
            {m.author === 'mentor' && <div className="at-message-label">Наставник</div>}
            <p>{m.text}</p>
          </div>
        ))}
        {busy && <p className="at-typing" role="status">Наставник печатает…</p>}
        {op?.status === 'failed' && (
          <div className="at-op-error" role="alert">
            <span>{op.error?.message ?? 'Наставник не ответил'}</span>
            <button className="at-secondary" onClick={retry}>Повторить</button>
          </div>
        )}
        {notice && <p className="at-notice" role="status">{notice}</p>}
        <div ref={end} />
      </div>
      <form className="at-composer" onSubmit={submit}>
        <textarea
          aria-label="Сообщение наставнику" placeholder="Спросите наставника…" rows={2}
          value={text} onChange={(e) => setText(e.target.value)} disabled={busy}
          onKeyDown={(e) => { if (e.key === 'Enter' && !e.shiftKey) submit(e) }}
        />
        <div className="at-composer-bottom">
          <span>Наставник объясняет, но не решает за вас</span>
          <button className="at-send" aria-label="Отправить наставнику" disabled={busy || !text.trim()}>↑</button>
        </div>
      </form>
    </div>
  )
}
