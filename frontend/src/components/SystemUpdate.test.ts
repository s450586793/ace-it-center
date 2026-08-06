import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { APIError, apiRequest } from '../api'
import type { SystemUpdateStatus } from '../types'
import SystemUpdate from './SystemUpdate.vue'

vi.mock('../api', async importOriginal => ({
  ...await importOriginal<typeof import('../api')>(),
  apiRequest: vi.fn(),
}))

const current = { backend: 'v0.4.0', web: 'v0.4.0' }
const latest = { backend: 'v0.4.1', web: 'v0.4.1', published_at: '2026-08-06T08:00:00Z' }

function status(overrides: Partial<SystemUpdateStatus> = {}): SystemUpdateStatus {
  return {
    current,
    latest,
    update_available: true,
    checked_at: '2026-08-06T08:00:00Z',
    ...overrides,
  }
}

function activeStatus(): SystemUpdateStatus {
  return status({
    task: {
      id: 'update-1', from: current, to: latest, stage: 'pulling',
      created_at: '2026-08-06T08:01:00Z', rolled_back: false, cleanup: 'not_run',
    },
  })
}

function mountUpdate() {
  return mount(SystemUpdate, {
    global: {
      stubs: {
        ElDialog: {
          props: ['modelValue', 'title'],
          template: '<section v-if="modelValue" role="dialog"><h2>{{ title }}</h2><slot /></section>',
        },
      },
    },
  })
}

describe('SystemUpdate', () => {
  beforeEach(() => {
    vi.mocked(apiRequest).mockResolvedValue(status())
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.mocked(apiRequest).mockReset()
  })

  it('checks the public update status on mount and renders compact public versions', async () => {
    const wrapper = mountUpdate()
    await flushPromises()

    expect(apiRequest).toHaveBeenCalledWith('/api/v1/system/update/check', { method: 'POST', body: '{}' })
    expect(wrapper.get('[data-version="current"]').text()).toContain('v0.4.0')
    expect(wrapper.get('[data-version="latest"]').text()).toContain('v0.4.1')
    expect(wrapper.text()).toContain(new Date(latest.published_at).toLocaleString('zh-CN', { hour12: false }))
    expect(wrapper.text()).not.toContain('sha256:')
    wrapper.unmount()
  })

  it('disables upgrades when the checked versions are equal', async () => {
    vi.mocked(apiRequest).mockResolvedValue(status({
      latest: { backend: 'v0.4.0', web: 'v0.4.0' }, update_available: false,
    }))
    const wrapper = mountUpdate()
    await flushPromises()

    expect(wrapper.get<HTMLButtonElement>('[data-action="start-update"]').element.disabled).toBe(true)
    wrapper.unmount()
  })

  it('opens an exact version confirmation and starts an available matching release only once', async () => {
    let resolveStart: ((value: unknown) => void) | undefined
    vi.mocked(apiRequest).mockImplementation((path, options) => {
      if (path === '/api/v1/system/update/check') return Promise.resolve(status())
      if (path === '/api/v1/system/update' && options?.method === 'POST') {
        return new Promise<unknown>(resolve => { resolveStart = resolve }) as Promise<never>
      }
      throw new Error(`unexpected request: ${path}`)
    })
    const wrapper = mountUpdate()
    await flushPromises()

    expect(wrapper.get<HTMLButtonElement>('[data-action="start-update"]').element.disabled).toBe(false)
    await wrapper.get('[data-action="start-update"]').trigger('click')
    expect(wrapper.get('[role="dialog"]').text()).toContain('v0.4.0')
    expect(wrapper.get('[role="dialog"]').text()).toContain('v0.4.1')

    await wrapper.get('.system-update-confirmation').trigger('submit.prevent')
    await wrapper.get('.system-update-confirmation').trigger('submit.prevent')
    expect(apiRequest).toHaveBeenCalledWith('/api/v1/system/update', {
      method: 'POST', body: JSON.stringify({ target_version: 'v0.4.1' }),
    })
    expect(apiRequest).toHaveBeenCalledTimes(2)

    resolveStart?.({
      id: 'update-1', from: current, to: latest, stage: 'checking',
      created_at: '2026-08-06T08:01:00Z', rolled_back: false, cleanup: 'not_run',
    })
    await flushPromises()
    wrapper.unmount()
  })

  it('polls an active update every two seconds and stops after unmount', async () => {
    vi.useFakeTimers()
    vi.mocked(apiRequest).mockResolvedValue(activeStatus())
    const wrapper = mountUpdate()
    await flushPromises()
    expect(apiRequest).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(2_000)
    await flushPromises()
    expect(apiRequest).toHaveBeenCalledWith('/api/v1/system/update')

    wrapper.unmount()
    await vi.advanceTimersByTimeAsync(6_000)
    expect(apiRequest).toHaveBeenCalledTimes(2)
  })

  it('keeps the last active stage through temporary service failures and retries with capped backoff', async () => {
    vi.useFakeTimers()
    vi.mocked(apiRequest)
      .mockResolvedValueOnce(activeStatus())
      .mockRejectedValueOnce(new APIError(503, 'service unavailable'))
      .mockResolvedValueOnce(activeStatus())
    const wrapper = mountUpdate()
    await flushPromises()

    await vi.advanceTimersByTimeAsync(2_000)
    await flushPromises()
    expect(wrapper.get('[data-stage="current"]').text()).toContain('正在拉取升级包')

    await vi.advanceTimersByTimeAsync(4_000)
    await flushPromises()
    expect(apiRequest).toHaveBeenCalledTimes(3)
    wrapper.unmount()
  })

  it('emits session expiry instead of showing an authorization error', async () => {
    vi.mocked(apiRequest).mockRejectedValue(new APIError(401, 'session expired'))
    const wrapper = mountUpdate()
    await flushPromises()

    expect(wrapper.emitted('session-expired')).toHaveLength(1)
    expect(wrapper.text()).not.toContain('session expired')
    wrapper.unmount()
  })

  it('stops active update polling after session expiry', async () => {
    vi.useFakeTimers()
    vi.mocked(apiRequest)
      .mockResolvedValueOnce(activeStatus())
      .mockRejectedValueOnce(new APIError(401, 'session expired'))
    const wrapper = mountUpdate()
    await flushPromises()

    await vi.advanceTimersByTimeAsync(2_000)
    await flushPromises()
    expect(wrapper.emitted('session-expired')).toHaveLength(1)

    await vi.advanceTimersByTimeAsync(6_000)
    expect(apiRequest).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })

  it('renders fixed DSM cleanup and recovery guidance without task error details', async () => {
    const wrapper = mountUpdate()
    vi.mocked(apiRequest).mockResolvedValueOnce(status({ task: {
      id: 'update-1', from: current, to: latest, stage: 'succeeded', created_at: '2026-08-06T08:01:00Z',
      rolled_back: false, cleanup: 'pending', error_code: 'cleanup_pending', error_message: 'sha256:secret token /srv/alias',
    } }))
    await wrapper.vm.$nextTick()
    await flushPromises()
    wrapper.unmount()

    const cleanup = mountUpdate()
    await flushPromises()
    expect(cleanup.text()).toContain('DSM')
    expect(cleanup.text()).not.toContain('sha256:secret')
    cleanup.unmount()
  })

  it('blocks mutations during active or manual intervention states and keeps long public text in wrapping structures', async () => {
    vi.mocked(apiRequest).mockResolvedValue(status({ task: {
      id: 'update-1', from: current, to: latest, stage: 'manual_intervention', created_at: '2026-08-06T08:01:00Z',
      rolled_back: true, cleanup: 'not_run', error_message: 'raw secret error',
    } }))
    const wrapper = mountUpdate()
    await flushPromises()

    expect(wrapper.text()).toContain('请在 DSM 中检查服务状态后联系管理员处理')
    expect(wrapper.text()).not.toContain('raw secret error')
    expect(wrapper.get<HTMLButtonElement>('[data-action="start-update"]').element.disabled).toBe(true)
    expect(wrapper.find('.system-update-version-value').exists()).toBe(true)
    expect(wrapper.find('.system-update-status-list').exists()).toBe(true)
    wrapper.unmount()
  })

  it('renders a fixed recovery action for failed updates without raw task errors', async () => {
    vi.mocked(apiRequest).mockResolvedValue(status({ task: {
      id: 'update-1', from: current, to: latest, stage: 'failed', created_at: '2026-08-06T08:01:00Z',
      rolled_back: true, cleanup: 'not_run', error_code: 'backend_unhealthy', error_message: 'raw /srv/secret token',
    } }))
    const wrapper = mountUpdate()
    await flushPromises()

    expect(wrapper.text()).toContain('请检查 DSM 服务状态后联系管理员处理')
    expect(wrapper.text()).not.toContain('raw /srv/secret token')
    wrapper.unmount()
  })
})
