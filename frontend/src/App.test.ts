import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'
import App from './App.vue'

const node = {
  id: 'node-1', group_id: 'group-1', name: 'finance-pc', type: 'windows' as const,
  agent_version: '0.1.0', os_name: 'Windows 11', os_version: '23H2', ip_address: '10.0.0.8',
  cpu_percent: 20.2, memory_percent: 55.6, disk_percent: 71.1,
  last_seen_at: '2026-07-28T00:00:00Z', created_at: '2026-07-27T00:00:00Z',
}

const pairing = {
  id: 'pairing-1', machine_id: 'machine-1', hostname: 'finance-pc', type: 'windows', agent_version: '0.3.2',
  state: 'pending' as const, created_at: '2026-07-30T00:00:00Z', expires_at: '2026-07-30T01:00:00Z',
}

const OperationsWorkspaceStub = defineComponent({
  props: { pairings: { type: Array, required: true } },
  emits: ['session-expired'],
  template: '<div data-testid="workspace-pairings">{{ pairings.map(pairing => pairing.hostname).join(",") }}</div>',
})

function jsonResponse(payload: unknown, status = 200) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

function requestPath(input: RequestInfo | URL) {
  return typeof input === 'string' ? input : input.toString()
}

function mockAuthenticatedPlatform() {
  return vi.spyOn(globalThis, 'fetch').mockImplementation(async input => {
    switch (requestPath(input)) {
      case '/api/v1/auth/status': return jsonResponse({ setup: true })
      case '/api/v1/auth/me': return jsonResponse({ owner: { id: 'owner-1', username: 'jarvis' } })
      case '/api/v1/organizations': return jsonResponse({ items: [] })
      case '/api/v1/sites': return jsonResponse({ items: [] })
      case '/api/v1/groups': return jsonResponse({ items: [] })
      case '/api/v1/nodes': return jsonResponse({ items: [node] })
      case '/api/v1/pairings': return jsonResponse({ items: [] })
      default: throw new Error(`Unexpected request: ${requestPath(input)}`)
    }
  })
}

function requestCount(fetchMock: ReturnType<typeof mockAuthenticatedPlatform>, path: string) {
  return fetchMock.mock.calls.filter(([input]) => requestPath(input) === path).length
}

function mountApp() {
  return mount(App, {
    global: {
      stubs: {
        OperationsWorkspace: OperationsWorkspaceStub,
      },
    },
  })
}

describe('App', () => {
  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('shows owner setup when the server is not initialized', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({ setup: false }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))

    const wrapper = mountApp()
    await flushPromises()

    expect(wrapper.text()).toContain('初始化 Owner')
    expect(wrapper.find('input[name="username"]').exists()).toBe(true)
  })

  it('refreshes nodes every 30 seconds without loading organizations or sites', async () => {
    vi.useFakeTimers()
    const fetchMock = mockAuthenticatedPlatform()
    const wrapper = mountApp()
    await flushPromises()
    expect(requestCount(fetchMock, '/api/v1/nodes')).toBe(1)
    expect(requestCount(fetchMock, '/api/v1/organizations')).toBe(0)
    expect(requestCount(fetchMock, '/api/v1/sites')).toBe(0)
    expect(requestCount(fetchMock, '/api/v1/groups')).toBe(1)

    await vi.advanceTimersByTimeAsync(30_000)
    await flushPromises()
    expect(requestCount(fetchMock, '/api/v1/nodes')).toBe(2)
    expect(requestCount(fetchMock, '/api/v1/organizations')).toBe(0)
    expect(requestCount(fetchMock, '/api/v1/sites')).toBe(0)
    wrapper.unmount()
  })

  it('refreshes nodes and pairings every 30 seconds only while the page is visible', async () => {
    vi.useFakeTimers()
    const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
    const fetchMock = mockAuthenticatedPlatform()
    const wrapper = mountApp()
    await flushPromises()

    expect(requestCount(fetchMock, '/api/v1/nodes')).toBe(1)
    expect(requestCount(fetchMock, '/api/v1/pairings')).toBe(1)
    await vi.advanceTimersByTimeAsync(30_000)
    await flushPromises()
    expect(requestCount(fetchMock, '/api/v1/nodes')).toBe(2)
    expect(requestCount(fetchMock, '/api/v1/pairings')).toBe(2)

    visibility.mockReturnValue('hidden')
    await vi.advanceTimersByTimeAsync(30_000)
    await flushPromises()
    expect(requestCount(fetchMock, '/api/v1/nodes')).toBe(2)
    expect(requestCount(fetchMock, '/api/v1/pairings')).toBe(2)
    wrapper.unmount()
  })

  it('does not overlap a pending pairing refresh', async () => {
    vi.useFakeTimers()
    let pairingRequests = 0
    let resolvePairing: ((response: Response) => void) | undefined
    const fetchMock = mockAuthenticatedPlatform()
    fetchMock.mockImplementation(input => {
      const path = requestPath(input)
      if (path === '/api/v1/pairings' && ++pairingRequests === 2) {
        return new Promise<Response>(resolve => { resolvePairing = resolve })
      }
      if (path === '/api/v1/pairings') return Promise.resolve(jsonResponse({ items: [] }))
      if (path === '/api/v1/nodes') return Promise.resolve(jsonResponse({ items: [node] }))
      if (path === '/api/v1/auth/status') return Promise.resolve(jsonResponse({ setup: true }))
      if (path === '/api/v1/auth/me') return Promise.resolve(jsonResponse({ owner: { id: 'owner-1', username: 'jarvis' } }))
      return Promise.resolve(jsonResponse({ items: [] }))
    })
    const wrapper = mountApp()
    await flushPromises()

    await vi.advanceTimersByTimeAsync(30_000)
    await vi.advanceTimersByTimeAsync(30_000)
    expect(requestCount(fetchMock, '/api/v1/pairings')).toBe(2)

    resolvePairing?.(jsonResponse({ items: [] }))
    await flushPromises()
    wrapper.unmount()
  })

  it('returns to login when pairing refresh is unauthorized', async () => {
    vi.useFakeTimers()
    let pairingRequests = 0
    const fetchMock = mockAuthenticatedPlatform()
    fetchMock.mockImplementation(input => {
      const path = requestPath(input)
      if (path === '/api/v1/pairings') {
        pairingRequests += 1
        return Promise.resolve(pairingRequests === 1 ? jsonResponse({ items: [] }) : jsonResponse({ error: 'session expired' }, 401))
      }
      if (path === '/api/v1/nodes') return Promise.resolve(jsonResponse({ items: [node] }))
      if (path === '/api/v1/auth/status') return Promise.resolve(jsonResponse({ setup: true }))
      if (path === '/api/v1/auth/me') return Promise.resolve(jsonResponse({ owner: { id: 'owner-1', username: 'jarvis' } }))
      return Promise.resolve(jsonResponse({ items: [] }))
    })
    const wrapper = mountApp()
    await flushPromises()

    await vi.advanceTimersByTimeAsync(30_000)
    await flushPromises()
    expect(wrapper.find('input[name="username"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('keeps the previous pairings and a safe warning when a pairing refresh fails before nodes finish', async () => {
    vi.useFakeTimers()
    let nodeRequests = 0
    let pairingRequests = 0
    let resolveNode: ((response: Response) => void) | undefined
    const fetchMock = mockAuthenticatedPlatform()
    fetchMock.mockImplementation(input => {
      const path = requestPath(input)
      if (path === '/api/v1/nodes' && ++nodeRequests === 2) {
        return new Promise<Response>(resolve => { resolveNode = resolve })
      }
      if (path === '/api/v1/nodes') return Promise.resolve(jsonResponse({ items: [node] }))
      if (path === '/api/v1/pairings') {
        pairingRequests += 1
        return Promise.resolve(pairingRequests === 1
          ? jsonResponse({ items: [pairing] })
          : jsonResponse({ error: 'database password leaked' }, 500))
      }
      if (path === '/api/v1/auth/status') return Promise.resolve(jsonResponse({ setup: true }))
      if (path === '/api/v1/auth/me') return Promise.resolve(jsonResponse({ owner: { id: 'owner-1', username: 'jarvis' } }))
      return Promise.resolve(jsonResponse({ items: [] }))
    })
    const wrapper = mountApp()
    await flushPromises()
    expect(wrapper.get('[data-testid="workspace-pairings"]').text()).toBe('finance-pc')

    await vi.advanceTimersByTimeAsync(30_000)
    await flushPromises()
    resolveNode?.(jsonResponse({ items: [node] }))
    await flushPromises()

    expect(wrapper.get('[data-testid="workspace-pairings"]').text()).toBe('finance-pc')
    expect(wrapper.get('[role="alert"]').text()).toBe('无法刷新待配对设备，请稍后重试')
    expect(wrapper.text()).not.toContain('database password leaked')
    wrapper.unmount()
  })

  it('does not overlap a pending node refresh', async () => {
    vi.useFakeTimers()
    let nodeRequests = 0
    let resolveRefresh: ((response: Response) => void) | undefined
    const fetchMock = mockAuthenticatedPlatform()
    fetchMock.mockImplementation(input => {
      if (requestPath(input) === '/api/v1/nodes' && ++nodeRequests === 2) {
        return new Promise<Response>(resolve => { resolveRefresh = resolve })
      }
      if (requestPath(input) === '/api/v1/nodes') return Promise.resolve(jsonResponse({ items: [node] }))
      return Promise.resolve(jsonResponse(requestPath(input) === '/api/v1/auth/status' ? { setup: true } : requestPath(input) === '/api/v1/auth/me' ? { owner: { id: 'owner-1', username: 'jarvis' } } : { items: [] }))
    })
    const wrapper = mountApp()
    await flushPromises()

    await vi.advanceTimersByTimeAsync(30_000)
    await vi.advanceTimersByTimeAsync(30_000)
    expect(requestCount(fetchMock, '/api/v1/nodes')).toBe(2)

    resolveRefresh?.(jsonResponse({ items: [node] }))
    await flushPromises()
    wrapper.unmount()
  })

  it('refreshes nodes immediately when a hidden tab becomes visible', async () => {
    vi.useFakeTimers()
    const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden')
    const fetchMock = mockAuthenticatedPlatform()
    const wrapper = mountApp()
    await flushPromises()
    expect(requestCount(fetchMock, '/api/v1/nodes')).toBe(1)

    visibility.mockReturnValue('visible')
    document.dispatchEvent(new Event('visibilitychange'))
    await flushPromises()
    expect(requestCount(fetchMock, '/api/v1/nodes')).toBe(2)
    wrapper.unmount()
  })

  it('returns to login and stops polling when node refresh is unauthorized', async () => {
    vi.useFakeTimers()
    let nodeRequests = 0
    const fetchMock = mockAuthenticatedPlatform()
    fetchMock.mockImplementation(async input => {
      const path = requestPath(input)
      if (path === '/api/v1/nodes') {
        nodeRequests += 1
        return nodeRequests === 1 ? jsonResponse({ items: [node] }) : jsonResponse({ error: 'session expired' }, 401)
      }
      if (path === '/api/v1/auth/status') return jsonResponse({ setup: true })
      if (path === '/api/v1/auth/me') return jsonResponse({ owner: { id: 'owner-1', username: 'jarvis' } })
      return jsonResponse({ items: [] })
    })
    const startInterval = vi.spyOn(globalThis, 'setInterval')
    const wrapper = mountApp()
    await flushPromises()
    const initialIntervalCount = startInterval.mock.calls.length

    await vi.advanceTimersByTimeAsync(30_000)
    await flushPromises()
    expect(wrapper.find('input[name="username"]').exists()).toBe(true)
    expect(startInterval).toHaveBeenCalledTimes(initialIntervalCount)
    wrapper.unmount()
  })

  it('does not start polling or leave a visibility listener when initial node loading is unauthorized', async () => {
    vi.useFakeTimers()
    const fetchMock = mockAuthenticatedPlatform()
    fetchMock.mockImplementation(async input => {
      if (requestPath(input) === '/api/v1/nodes') return jsonResponse({ error: 'session expired' }, 401)
      if (requestPath(input) === '/api/v1/auth/status') return jsonResponse({ setup: true })
      if (requestPath(input) === '/api/v1/auth/me') return jsonResponse({ owner: { id: 'owner-1', username: 'jarvis' } })
      return jsonResponse({ items: [] })
    })
    const addListener = vi.spyOn(document, 'addEventListener')
    const startInterval = vi.spyOn(globalThis, 'setInterval')
    const wrapper = mountApp()
    await flushPromises()

    expect(wrapper.find('input[name="username"]').exists()).toBe(true)
    expect(addListener).not.toHaveBeenCalledWith('visibilitychange', expect.any(Function))
    expect(startInterval).not.toHaveBeenCalled()

    await vi.advanceTimersByTimeAsync(30_000)
    document.dispatchEvent(new Event('visibilitychange'))
    await flushPromises()
    expect(requestCount(fetchMock, '/api/v1/nodes')).toBe(1)
    wrapper.unmount()
  })

  it('removes node polling timers and visibility listeners when unmounted', async () => {
    vi.useFakeTimers()
    const fetchMock = mockAuthenticatedPlatform()
    const clearInterval = vi.spyOn(globalThis, 'clearInterval')
    const removeListener = vi.spyOn(document, 'removeEventListener')
    const wrapper = mountApp()
    await flushPromises()
    wrapper.unmount()

    expect(clearInterval).toHaveBeenCalled()
    expect(removeListener).toHaveBeenCalledWith('visibilitychange', expect.any(Function))
    await vi.advanceTimersByTimeAsync(30_000)
    document.dispatchEvent(new Event('visibilitychange'))
    await flushPromises()
    expect(requestCount(fetchMock, '/api/v1/nodes')).toBe(1)
  })

  it('returns to login and stops node and pairing polling when system updates expire the session', async () => {
    vi.useFakeTimers()
    const fetchMock = mockAuthenticatedPlatform()
    const wrapper = mountApp()
    await flushPromises()
    expect(requestCount(fetchMock, '/api/v1/nodes')).toBe(1)
    expect(requestCount(fetchMock, '/api/v1/pairings')).toBe(1)

    wrapper.getComponent(OperationsWorkspaceStub).vm.$emit('session-expired')
    await flushPromises()

    expect(wrapper.find('input[name="username"]').exists()).toBe(true)
    await vi.advanceTimersByTimeAsync(30_000)
    await flushPromises()
    expect(requestCount(fetchMock, '/api/v1/nodes')).toBe(1)
    expect(requestCount(fetchMock, '/api/v1/pairings')).toBe(1)
    wrapper.unmount()
  })
})
