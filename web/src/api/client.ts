// Thin fetch client typed by src/api/schema.d.ts (generated from contracts/openapi.yaml).
// A contract change that breaks the UI fails `npm run typecheck`.
import type { components, paths } from './schema'

export type Schemas = components['schemas']
export type Account = Schemas['Account']

type Method = 'get' | 'post'
type Op<P extends keyof paths, M extends Method> = paths[P][M]
type Json<T> = T extends { content: { 'application/json': infer B } } ? B : undefined
type ReqBody<O> = O extends { requestBody: infer R } ? Json<R> : undefined
type Ok<O> = O extends { responses: infer R }
  ? R extends { 200: infer X } ? Json<X>
    : R extends { 201: infer X } ? Json<X>
      : R extends { 202: infer X } ? Json<X>
        : undefined
  : never

export class ApiError extends Error {
  constructor(readonly status: number, readonly code: string, message: string) {
    super(message)
  }
}

export async function api<P extends keyof paths, M extends Method>(
  method: M,
  path: P,
  body?: ReqBody<Op<P, M>>,
  params: Record<string, string> = {},
): Promise<Ok<Op<P, M>>> {
  const url = '/api' + String(path).replace(/\{(\w+)\}/g, (_, k: string) => encodeURIComponent(params[k]))
  const res = await fetch(url, {
    method: method.toUpperCase(),
    credentials: 'same-origin',
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  const data = res.status === 204 ? undefined : await res.json().catch(() => undefined)
  if (!res.ok) throw new ApiError(res.status, data?.code ?? 'http_' + res.status, data?.message ?? res.statusText)
  return data as Ok<Op<P, M>>
}
