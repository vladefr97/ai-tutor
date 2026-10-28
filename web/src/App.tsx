import { useEffect, useState } from 'react'
import { Link, Route, Routes, useNavigate } from 'react-router'
import { api, ApiError, type Account, type Schemas } from './api/client'
import CoursePage, { mentorNames, statusNames } from './Course'
import { directionIcons, Logo } from './icons'
import Login from './Login'
import { LearnerCourse, Learners } from './Teacher'

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
            {account.isTeacher && <Link className="at-quiet" to="/teacher">Кабинет преподавателя</Link>}
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
          <Route path="/courses/:id" element={<CoursePage />} />
          <Route path="/teacher" element={<Learners />} />
          <Route path="/teacher/courses/:id" element={<LearnerCourse />} />
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
  const navigate = useNavigate()
  const [courses, setCourses] = useState<Schemas['Course'][]>([])
  const [starting, setStarting] = useState(false)
  useEffect(() => { api('get', '/courses').then(setCourses) }, [])

  async function start(direction: Schemas['DirectionId']) {
    setStarting(true)
    try {
      const c = await api('post', '/courses', { direction })
      navigate(`/courses/${c.id}`)
    } finally {
      setStarting(false)
    }
  }

  return (
    <main className="at-main at-home">
      <div className="at-eyebrow">Ваша цель. Ваш темп.</div>
      <h1>Чему хотите научиться?</h1>
      <p className="at-subtitle">Выберите наставника. Вместе вы составите план и перейдёте к практике.</p>
      <div className="at-mentors">
        {mentors.map((m) => (
          <button key={m.id} className="at-mentor" onClick={() => start(m.id)} disabled={starting}>
            <span className="at-icon">{directionIcons[m.id]}</span>
            <h2>{m.name}</h2>
            <p>{m.desc}</p>
            <span className="at-mentor-foot">
              Начать разговор <span aria-hidden="true">↗</span>
            </span>
          </button>
        ))}
      </div>
      <p className="at-footnote">Начнём с того, что нужно именно вам.</p>
      {courses.length > 0 && (
        <section className="at-courses">
          <h2>Мои курсы</h2>
          {courses.map((c) => (
            <Link key={c.id} className="at-student" to={`/courses/${c.id}`}>
              <span>
                {mentorNames[c.direction].name}
                <small>{c.goal ?? 'Интервью'}</small>
              </span>
              <small>{statusNames[c.status]}</small>
            </Link>
          ))}
        </section>
      )}
    </main>
  )
}
