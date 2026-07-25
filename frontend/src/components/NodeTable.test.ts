import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import NodeTable from './NodeTable.vue'

describe('NodeTable', () => {
  it('renders online state and current resource snapshots', () => {
    const wrapper = mount(NodeTable, {
      props: {
        now: new Date('2026-07-26T02:00:00Z'),
        nodes: [
          {
            id: 'node-1', group_id: 'group-1', name: 'finance-pc', type: 'windows',
            agent_version: '0.1.0', os_name: 'Windows 11', os_version: '23H2', ip_address: '10.0.0.8',
            cpu_percent: 20.2, memory_percent: 55.6, disk_percent: 71.1,
            last_seen_at: '2026-07-26T01:59:30Z', created_at: '2026-07-25T00:00:00Z',
          },
          {
            id: 'node-2', group_id: 'group-1', name: 'backup-server', type: 'linux',
            agent_version: '0.1.0', os_name: 'Ubuntu', os_version: '24.04', ip_address: '10.0.0.9',
            cpu_percent: 3.1, memory_percent: 21.4, disk_percent: 39.2,
            last_seen_at: '2026-07-26T01:50:00Z', created_at: '2026-07-25T00:00:00Z',
          },
        ],
      },
    })

    expect(wrapper.text()).toContain('finance-pc')
    expect(wrapper.text()).toContain('在线')
    expect(wrapper.text()).toContain('20%')
    expect(wrapper.text()).toContain('backup-server')
    expect(wrapper.text()).toContain('离线')
  })
})
