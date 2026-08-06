import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { apiRequest } from '../api'
import type { NetworkHistoryResponse, Node } from '../types'
import NetworkHistoryDrawer from './NetworkHistoryDrawer.vue'

vi.mock('../api', () => ({ apiRequest: vi.fn() }))
enableAutoUnmount(afterEach)

const node: Node = {
  id: 'node-1', group_id: 'group-1', name: 'finance-pc', type: 'windows',
  agent_version: '0.3.0', os_name: 'Windows 11', os_version: '23H2', ip_address: '10.0.0.8',
  cpu_percent: 20.2, memory_percent: 55.6, disk_percent: 71.1,
  network_metrics_available: true, network_upload_mb_s: 1.25, network_download_mb_s: 8.75,
  last_seen_at: '2026-07-28T11:59:30Z', created_at: '2026-07-25T00:00:00Z',
}

function response(range: NetworkHistoryResponse['range'], download = 4.8): NetworkHistoryResponse {
  return {
    node_id: node.id,
    range,
    unit: 'MB/s',
    points: [{
      captured_at: '2026-07-28T12:00:00Z',
      upload_avg_mb_s: 1.2,
      download_avg_mb_s: download,
      upload_peak_mb_s: 2.1,
      download_peak_mb_s: 8.4,
    }],
  }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((onResolve, onReject) => {
    resolve = onResolve
    reject = onReject
  })
  return { promise, resolve, reject }
}

function mountDrawer() {
  return mount(NetworkHistoryDrawer, {
    props: { modelValue: true, node },
    global: {
      stubs: {
        ElDrawer: { template: '<aside><slot name="header"/><slot/></aside>' },
        NetworkHistoryChart: { props: ['points'], template: '<div class="chart-stub">{{ points[0]?.download_avg_mb_s }}</div>' },
      },
    },
  })
}

describe('NetworkHistoryDrawer', () => {
  afterEach(() => {
    vi.mocked(apiRequest).mockReset()
  })

  it('loads 24h initially and exposes all four ranges', async () => {
    vi.mocked(apiRequest).mockResolvedValue(response('24h'))
    const wrapper = mountDrawer()
    await flushPromises()

    expect(apiRequest).toHaveBeenCalledWith('/api/v1/nodes/node-1/network-history?range=24h')
    expect(wrapper.findAll('[data-range]')).toHaveLength(4)
    expect(wrapper.get('[data-range="24h"]').attributes('aria-pressed')).toBe('true')
    expect(wrapper.text()).toContain('平均下载')
    expect(wrapper.text()).toContain('峰值上传')
  })

  it('loads a changed range and ignores an older response that finishes last', async () => {
    const first = deferred<NetworkHistoryResponse>()
    const second = deferred<NetworkHistoryResponse>()
    vi.mocked(apiRequest).mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)
    const wrapper = mountDrawer()

    await wrapper.get('[data-range="7d"]').trigger('click')
    expect(apiRequest).toHaveBeenLastCalledWith('/api/v1/nodes/node-1/network-history?range=7d')

    second.resolve(response('7d', 7.7))
    await flushPromises()
    expect(wrapper.get('.chart-stub').text()).toBe('7.7')

    first.resolve(response('24h', 24))
    await flushPromises()
    expect(wrapper.get('.chart-stub').text()).toBe('7.7')
  })

  it('renders loading, empty, error, and recovered history states', async () => {
    const pending = deferred<NetworkHistoryResponse>()
    vi.mocked(apiRequest).mockReturnValueOnce(pending.promise)
    const wrapper = mountDrawer()
    await wrapper.vm.$nextTick()
    expect(wrapper.get('[role="status"]').text()).toContain('正在加载')

    pending.resolve({ ...response('24h'), points: [] })
    await flushPromises()
    expect(wrapper.text()).toContain('暂无网络历史数据')

    vi.mocked(apiRequest).mockRejectedValueOnce(new Error('history unavailable'))
    await wrapper.get('[data-range="7d"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('history unavailable')

    vi.mocked(apiRequest).mockResolvedValueOnce(response('7d'))
    await wrapper.get('[data-action="retry"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.find('.chart-stub').exists()).toBe(true)
  })
})
