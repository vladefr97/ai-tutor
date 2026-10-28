import { useEffect, useState } from 'react'
import { Link, Route, Routes, useNavigate, useSearchParams } from 'react-router'
import { api, ApiError, type Account, type Schemas } from './api/client'
import { directionIcons, Logo } from './icons'
import Login from './Login'

export default function App() {
  const [account, setAccount] = useState<Account | null | undefined>(undefined)
  const navigate = useNavigate()

  useEffect(() => {
    api('get', '/me').then(setAccount, (e) => {
      if (e instanceof ApiError && e.status === 401) setAccount(null)
      else throw e
    })
  }, [])

  async function logout() {
    await api('post', '/auth/logout')
    setAccount(null)
    navigate('/')
  }

  return (
    <>
      <header className="at-header">
        <Link className="at-brand" to="/" aria-label="ai-tutor — главная">
          <Logo />
          <span>ai-tutor</span>
        </Link>
        {account && (
          <div className="at-header-right">
            <span className="at-quiet at-email">{account.email}</span>
            <button className="at-quiet" onClick={logout}>Выйти</button>
          </div>
        )}
      </header>
      {account === undefined ? null : account === null ? (
        <Login onLogin={setAccount} />
      ) : (
        <Routes>
          <Route path="/" element={<Mentors />} />
          <Route path="/courses/new" element={<NewCourse />} />
        </Routes>
      )}
    </>
  )
}

const mentors: { id: Schemas['DirectionId']; name: string; desc: string }[] = [
  { id: 'python', name: 'Python Developer', desc: 'От первых функций до своих приложений. Код, практика и ревью.' },
  { id: 'git', name: 'Наставник по Git', desc: 'Коммиты, ветки и pull request. Освойте привычный рабочий процесс.' },
  { id: 'pm', name: 'Project Manager', desc: 'Цели, задачи и команда. Учитесь управлять проектом на практике.' },
]

function Mentors() {
  return (
    <main className="at-main at-home">
      <div className="at-eyebrow">Ваша цель. Ваш темп.</div>
      <h1>Чему хотите научиться?</h1>
      <p className="at-subtitle">Выберите наставника. Вместе вы составите план и перейдёте к практике.</p>
      <div className="at-mentors">
        {mentors.map((m) => (
          <Link key={m.id} className="at-mentor" to={`/courses/new?direction=${m.id}`}>
            <span className="at-icon">{directionIcons[m.id]}</span>
            <h2>{m.name}</h2>
            <p>{m.desc}</p>
            <span className="at-mentor-foot">
              Начать разговор <span aria-hidden="true">↗</span>
            </span>
          </Link>
        ))}
      </div>
      <p className="at-footnote">Начнём с того, что нужно именно вам.</p>
    </main>
  )
}

// ponytail: placeholder until #12 wires the interview chat.
function NewCourse() {
  const direction = useSearchParams()[0].get('direction')
  const mentor = mentors.find((m) => m.id === direction)
  return (
    <main className="at-main">
      <Link className="at-back" to="/">← Все наставники</Link>
      <h1>{mentor?.name ?? 'Наставник'}</h1>
      <p className="at-subtitle">Скоро здесь начнётся разговор с наставником.</p>
    </main>
  )
}
