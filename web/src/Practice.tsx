import { useEffect, useState } from 'react'
import Markdown from 'react-markdown'
import { api, type Schemas } from './api/client'

export type Task = { id: string; title: string; kind: 'case' | 'code'; statement: string; relatedPages: string[]; repoPath?: string }
type Attempt = Schemas['Attempt']

const POLL_MS = 2000

// Practice tab: PM cases are submitted on the site and reviewed by the Work
// reviewer; code tasks are solved in the practice repository via pull request.
export default function Practice({ courseId, tasks, pageTitle, attempts, onAttempt, openPage }: {
  courseId: string
  tasks: Task[]
  pageTitle: (id: string) => string | undefined
  attempts: Attempt[]
  onAttempt: (a: Attempt) => void
  openPage: (id: string) => void
}) {
  if (tasks.length === 0) return <p className="at-muted">В этом курсе практики нет: план предусматривает теорию и итоговый экзамен.</p>
  return (
    <div className="at-practice">
      {tasks.map((t) => (
        <section key={t.id} className="at-lesson at-task" aria-label={'Задание: ' + t.title}>
          <h2>{t.title}</h2>
          <div className="at-md"><Markdown>{t.statement}</Markdown></div>
          {t.relatedPages.length > 0 && (
            <p className="at-small at-muted">
              Материал:{' '}
              {t.relatedPages.map((p, i) => (
                <span key={p}>
                  {i > 0 && ', '}
                  <button className="at-quiet at-inline-link" onClick={() => openPage(p)}>{pageTitle(p) ?? p}</button>
                </span>
              ))}
            </p>
          )}
          {t.kind === 'code' ? (
            <p className="at-notice">Решение отправляется через pull request в учебный репозиторий.</p>
          ) : (
            <CaseForm courseId={courseId} task={t} attempts={attempts.filter((a) => a.itemId === t.id)} onAttempt={onAttempt} />
          )}
        </section>
      ))}
    </div>
  )
}

const verdict = (a: Attempt) =>
  a.status === 'passed' ? '✓ Работа зачтена' : a.status === 'failed' ? 'Нужны исправления' : a.status === 'error' ? 'Не удалось проверить работу. Отправьте ещё раз.' : 'Проверяется…'

function CaseForm({ courseId, task, attempts, onAttempt }: {
  courseId: string; task: Task; attempts: Attempt[]; onAttempt: (a: Attempt) => void
}) {
  const sorted = [...attempts].sort((a, b) => a.createdAt.localeCompare(b.createdAt))
  const last = sorted.at(-1)
  const [text, setText] = useState(last?.answer.text ?? '')
  const pending = last?.status === 'pending'

  useEffect(() => {
    if (!last || last.status !== 'pending') return
    const t = setTimeout(async () => onAttempt(await api('get', '/attempts/{attemptId}', undefined, { attemptId: last.id })), POLL_MS)
    return () => clearTimeout(t)
  }, [last, onAttempt])

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    onAttempt(await api('post', '/courses/{courseId}/attempts', { itemId: task.id, answer: { text: text.trim() } }, { courseId }))
  }

  return (
    <form onSubmit={submit} aria-label={'Ответ на задание: ' + task.title}>
      <textarea className="at-answer" aria-label="Ваше решение" value={text} maxLength={20000} rows={10}
        placeholder="Опишите цель, задачи, сроки и риски…" onChange={(e) => setText(e.target.value)} />
      <div className="at-actions">
        <button className="at-primary" disabled={pending || !text.trim() || last?.status === 'passed'}>Отправить на ревью →</button>
      </div>
      {last && (
        <div role="status" className={'at-notice' + (last.status === 'passed' ? '' : ' is-wrong')}>
          {verdict(last)}
          {last.feedback && <p>{last.feedback}</p>}
        </div>
      )}
      {sorted.length > 1 && (
        <details className="at-history">
          <summary>История попыток ({sorted.length})</summary>
          <ol>
            {sorted.map((a) => (
              <li key={a.id}>
                <span className="at-small at-muted">{new Date(a.createdAt).toLocaleString('ru-RU')} — {verdict(a)}</span>
                <p className="at-small">{a.answer.text}</p>
                {a.feedback && <p className="at-small at-muted">{a.feedback}</p>}
              </li>
            ))}
          </ol>
        </details>
      )}
    </form>
  )
}
