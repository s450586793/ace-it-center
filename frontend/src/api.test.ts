import { afterEach, describe, expect, it, vi } from 'vitest'
import { apiRequest } from './api'

describe('apiRequest', () => {
  afterEach(() => vi.restoreAllMocks())

  it('returns decoded JSON for a successful response', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({ status: 'ok' }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))

    await expect(apiRequest<{ status: string }>('/api/v1/health')).resolves.toEqual({ status: 'ok' })
  })

  it('uses the public API error without exposing an HTML response', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValueOnce(new Response(JSON.stringify({ error: 'unauthorized' }), {
      status: 401,
      headers: { 'Content-Type': 'application/json' },
    })).mockResolvedValueOnce(new Response('<html>internal stack trace</html>', {
      status: 500,
      headers: { 'Content-Type': 'text/html' },
    }))

    await expect(apiRequest('/private')).rejects.toThrow('unauthorized')
    await expect(apiRequest('/broken')).rejects.toThrow('Request failed (500)')
  })
})

