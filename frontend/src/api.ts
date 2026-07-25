export class APIError extends Error {
  constructor(public readonly status: number, message: string) {
    super(message)
    this.name = 'APIError'
  }
}

export async function apiRequest<T>(path: string, options: RequestInit = {}): Promise<T> {
  const headers = new Headers(options.headers)
  if (options.body && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }
  const response = await fetch(path, {
    ...options,
    headers,
    credentials: 'same-origin',
  })

  if (!response.ok) {
    const contentType = response.headers.get('Content-Type') ?? ''
    if (contentType.includes('application/json')) {
      const payload = await response.json() as { error?: string }
      throw new APIError(response.status, payload.error || `Request failed (${response.status})`)
    }
    throw new APIError(response.status, `Request failed (${response.status})`)
  }
  if (response.status === 204) return undefined as T
  return await response.json() as T
}

