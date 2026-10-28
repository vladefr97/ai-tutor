import { expect } from '@playwright/test'

const MAILPIT = process.env.MAILPIT_URL ?? 'http://localhost:8025'

// Polls the local Mailpit inbox for the newest one-time code sent to `email`.
export async function readCode(email: string): Promise<string> {
  let code = ''
  await expect
    .poll(async () => {
      const search = await fetch(`${MAILPIT}/api/v1/search?query=${encodeURIComponent('to:' + email)}`).then((r) => r.json())
      const id = search.messages?.[0]?.ID
      if (!id) return ''
      const msg = await fetch(`${MAILPIT}/api/v1/message/${id}`).then((r) => r.json())
      code = msg.Text?.match(/\b\d{6}\b/)?.[0] ?? ''
      return code
    }, { timeout: 15_000 })
    .not.toBe('')
  return code
}
