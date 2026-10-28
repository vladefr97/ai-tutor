import { useState, type FormEvent } from 'react'
import { api, ApiError, type Account } from './api/client'

export default function Login({ onLogin }: { onLogin: (a: Account) => void }) {
  const [email, setEmail] = useState('')
  const [code, setCode] = useState('')
  const [sent, setSent] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  async function run(action: () => Promise<void>) {
    setBusy(true)
    setError('')
    try {
      await action()
    } catch (e) {
      if (!(e instanceof ApiError)) setError('Не удалось связаться с сервером. Попробуйте ещё раз.')
      else if (sent) setError('Неверный или истёкший код')
      else setError('Проверьте адрес почты')
    } finally {
      setBusy(false)
    }
  }

  const requestCode = (e: FormEvent) => {
    e.preventDefault()
    run(async () => {
      await api('post', '/auth/code', { email })
      setSent(true)
    })
  }
  const verify = (e: FormEvent) => {
    e.preventDefault()
    run(async () => onLogin(await api('post', '/auth/verify', { email, code })))
  }

  return (
    <main className="at-main at-login">
      <div className="at-eyebrow">Вход</div>
      <h1>{sent ? 'Введите код' : 'Войдите по почте'}</h1>
      <p className="at-subtitle">
        {sent ? `Мы отправили шестизначный код на ${email}.` : 'Пароль не нужен: пришлём одноразовый код.'}
      </p>
      {!sent ? (
        <form className="at-form" onSubmit={requestCode}>
          <label htmlFor="email">Почта</label>
          <input id="email" type="email" required autoComplete="email" value={email} onChange={(e) => setEmail(e.target.value)} />
          <button className="at-primary" disabled={busy}>Получить код</button>
        </form>
      ) : (
        <form className="at-form" onSubmit={verify}>
          <label htmlFor="code">Код из письма</label>
          <input id="code" inputMode="numeric" autoComplete="one-time-code" pattern="[0-9]{6}" maxLength={6} required value={code} onChange={(e) => setCode(e.target.value.replace(/\D/g, ''))} />
          <button className="at-primary" disabled={busy}>Войти</button>
          <button type="button" className="at-quiet" onClick={() => { setSent(false); setCode(''); setError('') }}>Другая почта</button>
        </form>
      )}
      {error && <p className="at-error" role="alert">{error}</p>}
    </main>
  )
}
