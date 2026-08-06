import { enableAutoUnmount, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { apiRequest } from '../api'
import type { Node } from '../types'
import NetworkMonitoring from './NetworkMonitoring.vue'

vi.mock('../api', () => ({ apiRequest: vi.fn() }))
enableAutoUnmount(afterEach)

const now = new Date('2026-08-03T04:00:00Z')
const nodes: Node[] = [
  {
    id: 'node-1', group_id: 'group-1', name: 'finance-pc', type: 'windows',
    agent_version: '0.3.8', os_name: 'Windows 11', os_version: '23H2', ip_address: '10.0.0.8',
    cpu_percent: 20.2, memory_percent: 55.6, disk_percent: 71.1,
    network_metrics_available: true, network_upload_mb_s: 1.25, network_download_mb_s: 8.75,
    network_usage_available: true, network_usage_day: '2026-08-03',
    network_today_upload_bytes: 3_000_000, network_today_download_bytes: 8_000_000,
    network_month_upload_bytes: 13_000_000, network_month_download_bytes: 28_000_000,
    last_seen_at: '2026-08-03T03:59:30Z', created_at: '2026-07-25T00:00:00Z',
  },
  {
    id: 'node-2', group_id: 'group-1', name: 'legacy-pc', type: 'windows',
    agent_version: '0.3.7', os_name: 'Windows 10', os_version: '22H2', ip_address: '10.0.0.9',
    cpu_percent: 10, memory_percent: 40, disk_percent: 50,
    network_metrics_available: false, network_upload_mb_s: 100, network_download_mb_s: 200,
    network_usage_available: false, network_usage_day: '',
    network_today_upload_bytes: 0, network_today_download_bytes: 0,
    network_month_upload_bytes: 0, network_month_download_bytes: 0,
    last_seen_at: '2026-08-03T03:59:20Z', created_at: '2026-07-25T00:00:00Z',
  },
  {
    id: 'node-3', group_id: 'group-1', name: 'archive', type: 'linux',
    agent_version: '0.3.8', os_name: 'Ubuntu', os_version: '24.04', ip_address: '10.0.0.10',
    cpu_percent: 4, memory_percent: 20, disk_percent: 30,
    network_metrics_available: true, network_upload_mb_s: 0.75, network_download_mb_s: 1.25,
    network_usage_available: true, network_usage_day: '2026-08-03',
    network_today_upload_bytes: 2_000, network_today_download_bytes: 4_000,
    network_month_upload_bytes: 12_000, network_month_download_bytes: 24_000,
    last_seen_at: '2026-08-03T03:50:00Z', created_at: '2026-07-25T00:00:00Z',
  },
]

function mountMonitoring(componentNodes = nodes) {
  return mount(NetworkMonitoring, {
    props: { nodes: componentNodes, now },
    global: {
      stubs: {
        NetworkHistoryDrawer: {
          props: ['modelValue', 'node'],
          template: '<aside v-if="modelValue" data-testid="history-drawer">{{ node.name }}</aside>',
        },
      },
    },
  })
}

describe('NetworkMonitoring', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    vi.mocked(apiRequest).mockReset()
  })

  it('renders the confirmed eight columns without requesting a 24-hour summary', () => {
    const wrapper = mountMonitoring()
    const headings = wrapper.findAll('thead th').map(cell => cell.text())

    expect(headings).toEqual(['设备', '状态', '当前下载', '当前上传', '今日总下载', '今日总上传', '本月总下载', '本月总上传'])
    expect(apiRequest).not.toHaveBeenCalled()
    expect(wrapper.get('[data-metric="download-total"]').text()).toContain('8.75 MB/s')
    expect(wrapper.get('[data-metric="upload-total"]').text()).toContain('1.25 MB/s')
    expect(wrapper.get('[data-metric="capable-online"]').text()).toContain('1 / 2')
  })

  it('shows current rates and Agent-local day and month totals', () => {
    const wrapper = mountMonitoring()
    const row = wrapper.get('tr[data-node-id="node-1"]')

    expect(row.get('td[data-label="当前下载"]').text()).toBe('8.75 MB/s')
    expect(row.get('td[data-label="当前上传"]').text()).toBe('1.25 MB/s')
    expect(row.get('td[data-label="今日总下载"]').text()).toBe('8.00 MB')
    expect(row.get('td[data-label="今日总上传"]').text()).toBe('3.00 MB')
    expect(row.get('td[data-label="本月总下载"]').text()).toBe('28.00 MB')
    expect(row.get('td[data-label="本月总上传"]').text()).toBe('13.00 MB')
  })

  it('hides stale current rates but retains same-period totals for offline nodes', () => {
    const wrapper = mountMonitoring()
    const row = wrapper.get('tr[data-node-id="node-3"]')

    expect(row.get('td[data-label="当前下载"]').text()).toBe('-')
    expect(row.get('td[data-label="当前上传"]').text()).toBe('-')
    expect(row.get('td[data-label="今日总下载"]').text()).toBe('4.00 KB')
    expect(row.get('td[data-label="本月总上传"]').text()).toBe('12.00 KB')
  })

  it('shows zero for a new offline period and a dash for legacy Agents', () => {
    const previousMonth = { ...nodes[2], id: 'node-4', network_usage_day: '2026-07-31' }
    const wrapper = mountMonitoring([nodes[1], previousMonth])

    const legacy = wrapper.get('tr[data-node-id="node-2"]')
    expect(legacy.get('td[data-label="今日总下载"]').text()).toBe('-')
    expect(legacy.get('td[data-label="本月总上传"]').text()).toBe('-')

    const expired = wrapper.get('tr[data-node-id="node-4"]')
    expect(expired.get('td[data-label="今日总下载"]').text()).toBe('0 B')
    expect(expired.get('td[data-label="本月总上传"]').text()).toBe('0 B')
  })

  it('keeps month totals when only the Beijing day has rolled', () => {
    const previousDay = { ...nodes[2], id: 'node-5', network_usage_day: '2026-08-02' }
    const wrapper = mountMonitoring([previousDay])
    const row = wrapper.get('tr[data-node-id="node-5"]')

    expect(row.get('td[data-label="今日总下载"]').text()).toBe('0 B')
    expect(row.get('td[data-label="本月总下载"]').text()).toBe('24.00 KB')
  })

  it('opens history only for a metrics-capable row', async () => {
    const wrapper = mountMonitoring()

    await wrapper.get('tr[data-node-id="node-2"]').trigger('click')
    expect(wrapper.find('[data-testid="history-drawer"]').exists()).toBe(false)

    await wrapper.get('tr[data-node-id="node-1"]').trigger('click')
    expect(wrapper.get('[data-testid="history-drawer"]').text()).toBe('finance-pc')
  })

  it('renders an empty state without requesting a summary', () => {
    const wrapper = mountMonitoring([])

    expect(wrapper.text()).toContain('尚未接入设备')
    expect(apiRequest).not.toHaveBeenCalled()
  })
})
