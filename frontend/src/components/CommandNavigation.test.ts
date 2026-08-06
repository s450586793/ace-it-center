import { mount } from '@vue/test-utils'
import { afterEach, expect, it, vi } from 'vitest'
import type { Node } from '../types'
import OperationsWorkspace from './OperationsWorkspace.vue'

const node: Node = {
  id: 'node-1', group_id: 'group-1', name: 'finance-pc', type: 'windows', agent_version: '0.4.0',
  os_name: 'Windows 11', os_version: '23H2', ip_address: '192.168.1.10', cpu_percent: 10,
  memory_percent: 20, disk_percent: 30, network_metrics_available: true,
  network_upload_mb_s: 1, network_download_mb_s: 2,
  last_seen_at: new Date().toISOString(), created_at: '2026-08-03T00:00:00Z',
}

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

it('opens and unmounts the command center from primary navigation', async () => {
  vi.stubGlobal('matchMedia', vi.fn(() => ({ matches: false })))
  vi.spyOn(globalThis, 'fetch').mockRejectedValue(new Error('manifest unavailable'))
  const wrapper = mount(OperationsWorkspace, {
    props: {
      owner: { id: 'owner-1', username: 'jarvis' },
      groups: [], nodes: [node], pairings: [],
    },
    global: {
      stubs: {
        CommandCenter: { props: ['nodes'], template: '<section data-testid="command-center">{{ nodes.length }}</section>' },
        AgentDownloads: true,
        NetworkMonitoring: true,
        NodeTable: true,
        PairingRequests: true,
        ElDialog: { template: '<div><slot /></div>' },
      },
    },
  })

  const navigation = wrapper.get('a[href="#commands"]')
  await navigation.trigger('click')
  expect(navigation.attributes('aria-current')).toBe('page')
  expect(wrapper.get('.section-index').text()).toBe('INFRASTRUCTURE / COMMANDS')
  expect(wrapper.get('h1').text()).toBe('命令中心')
  expect(wrapper.get('[data-testid="command-center"]').text()).toBe('1')

  await wrapper.get('a[href="#nodes"]').trigger('click')
  expect(wrapper.find('[data-testid="command-center"]').exists()).toBe(false)
  wrapper.unmount()
})
