import { expect } from '@playwright/test'

const MAILPIT = process.env.MAILPIT_URL ?? 'http://localhost:8025'
const consumed = new Set<string>()

// Polls the local Mailpit inbox for the newest one-time code sent to `email`
// that this test run has not used yet (a repeated login must wait for its own letter).
export async function readCode(email: string): Promise<string> {
  let code = ''
  await expect
    .poll(async () => {
      const search = await fetch(`${MAILPIT}/api/v1/search?query=${encodeURIComponent('to:' + email)}`).then((r) => r.json())
      const id: string | undefined = search.messages?.[0]?.ID
      if (!id || consumed.has(id)) return ''
      const msg = await fetch(`${MAILPIT}/api/v1/message/${id}`).then((r) => r.json())
      code = msg.Text?.match(/\b\d{6}\b/)?.[0] ?? ''
      if (code) consumed.add(id)
      return code
    }, { timeout: 15_000 })
    .not.toBe('')
  return code
}
