import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { apiRequest } from '../api'
import NodeTable from './NodeTable.vue'

vi.mock('../api', () => ({ apiRequest: vi.fn() }))
vi.mock('element-plus', () => ({
  ElDialog: {
    props: { modelValue: Boolean },
    emits: ['update:modelValue'],
    template: '<div v-if="modelValue" class="el-dialog"><slot /></div>',
  },
}))

const groups = [
  { id: 'group-1', name: '办公电脑', created_at: '2026-07-25T00:00:00Z' },
  { id: 'group-2', name: '生产服务器', created_at: '2026-07-25T00:00:00Z' },
]

describe('NodeTable', () => {
  afterEach(() => {
    vi.mocked(apiRequest).mockReset()
  })

  it('renders online state and current resource snapshots', () => {
    const wrapper = mount(NodeTable, {
      props: {
        groups,
        now: new Date('2026-07-26T02:00:00Z'),
        nodes: [
          {
            id: 'node-1', group_id: 'group-1', name: 'finance-pc', type: 'windows',
            agent_version: '0.1.0', os_name: 'Windows 11', os_version: '23H2', ip_address: '10.0.0.8',
            cpu_percent: 20.2, memory_percent: 55.6, disk_percent: 71.1,
            network_metrics_available: true, network_upload_mb_s: 1.25, network_download_mb_s: 8.75,
            last_seen_at: '2026-07-26T01:59:30Z', created_at: '2026-07-25T00:00:00Z',
          },
          {
            id: 'node-2', group_id: 'group-1', name: 'backup-server', type: 'linux',
            agent_version: '0.1.0', os_name: 'Ubuntu', os_version: '24.04', ip_address: '10.0.0.9',
            cpu_percent: 3.1, memory_percent: 21.4, disk_percent: 39.2,
            network_metrics_available: false, network_upload_mb_s: 0, network_download_mb_s: 0,
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

  it('renders network rates with arrows and without direction words', () => {
    const wrapper = mount(NodeTable, {
      props: {
        groups,
        now: new Date('2026-07-26T02:00:00Z'),
        nodes: [{
          id: 'node-1', group_id: 'group-1', name: 'finance-pc', type: 'windows',
          agent_version: '0.1.0', os_name: 'Windows 11', os_version: '23H2', ip_address: '10.0.0.8',
          cpu_percent: 20.2, memory_percent: 55.6, disk_percent: 71.1,
          network_metrics_available: true, network_upload_mb_s: 1.25, network_download_mb_s: 8.75,
          last_seen_at: '2026-07-26T01:59:30Z', created_at: '2026-07-25T00:00:00Z',
        }],
      },
    })

    const networkCell = wrapper.get('td[data-label="网络"]')
    expect(networkCell.text()).toContain('↓ 8.75 MB/s')
    expect(networkCell.text()).toContain('↑ 1.25 MB/s')
    expect(networkCell.text()).not.toContain('下载')
    expect(networkCell.text()).not.toContain('上传')
  })

  it('renders a saved device remark', () => {
    const wrapper = mount(NodeTable, {
      props: {
        groups,
        now: new Date('2026-07-26T02:00:00Z'),
        nodes: [{
          id: 'node-1', group_id: 'group-1', remark: '15 楼财务电脑', name: 'finance-pc', type: 'windows',
          agent_version: '0.1.0', os_name: 'Windows 11', os_version: '23H2', ip_address: '10.0.0.8',
          cpu_percent: 20.2, memory_percent: 55.6, disk_percent: 71.1,
          network_metrics_available: true, network_upload_mb_s: 1.25, network_download_mb_s: 8.75,
          last_seen_at: '2026-07-26T01:59:30Z', created_at: '2026-07-25T00:00:00Z',
        }],
      },
    })

    expect(wrapper.get('td[data-label="备注"]').text()).toBe('15 楼财务电脑')
  })

  it('edits and saves a device remark', async () => {
    vi.mocked(apiRequest).mockResolvedValue({ node: { id: 'node-1', remark: '仓库值班电脑' } })
    const wrapper = mount(NodeTable, {
      props: {
        groups,
        now: new Date('2026-07-26T02:00:00Z'),
        nodes: [{
          id: 'node-1', group_id: 'group-1', remark: '原备注', name: 'finance-pc', type: 'windows',
          agent_version: '0.3.3', os_name: 'Windows 11', os_version: '25H2', ip_address: '192.168.31.25',
          cpu_percent: 20.2, memory_percent: 55.6, disk_percent: 71.1,
          network_metrics_available: true, network_upload_mb_s: 1.25, network_download_mb_s: 8.75,
          last_seen_at: '2026-07-26T01:59:30Z', created_at: '2026-07-25T00:00:00Z',
        }],
      },
    })

    await wrapper.get('button[aria-label="编辑 finance-pc 的备注"]').trigger('click')
    expect(wrapper.get<HTMLTextAreaElement>('#node-remark').element.value).toBe('原备注')
    await wrapper.get<HTMLTextAreaElement>('#node-remark').setValue('仓库值班电脑')
    await wrapper.get('form.remark-form').trigger('submit.prevent')
    await flushPromises()

    expect(apiRequest).toHaveBeenCalledWith('/api/v1/nodes/node-1', {
      method: 'PATCH', body: JSON.stringify({ remark: '仓库值班电脑' }),
    })
    expect(wrapper.emitted('updated')).toHaveLength(1)
  })

  it('loads and displays the latest device log snapshot', async () => {
	vi.mocked(apiRequest).mockResolvedValue({
	  node_id: 'node-1', agent_log: 'agent log tail', update_log: 'update log tail', captured_at: '2026-08-01T02:00:00Z',
	})
	const wrapper = mount(NodeTable, {
	  props: {
		groups,
		now: new Date('2026-08-01T02:00:00Z'),
		nodes: [{
		  id: 'node-1', group_id: 'group-1', name: 'finance-pc', type: 'windows',
		  agent_version: '0.3.7', os_name: 'Windows 11', os_version: '25H2', ip_address: '192.168.31.25',
		  cpu_percent: 20.2, memory_percent: 55.6, disk_percent: 71.1,
		  network_metrics_available: true, network_upload_mb_s: 1.25, network_download_mb_s: 8.75,
		  last_seen_at: '2026-08-01T01:59:30Z', created_at: '2026-07-25T00:00:00Z',
		}],
	  },
	})

	await wrapper.get('button[aria-label="查看 finance-pc 的日志"]').trigger('click')
	await flushPromises()

	expect(apiRequest).toHaveBeenCalledWith('/api/v1/nodes/node-1/logs')
	expect(wrapper.text()).toContain('agent.log')
	expect(wrapper.text()).toContain('agent log tail')
	await wrapper.get('button[aria-label="查看 update.log"]').trigger('click')
	expect(wrapper.text()).toContain('update log tail')
  })

  it('keeps the remark editor open and shows a save error', async () => {
    vi.mocked(apiRequest).mockRejectedValue(new Error('保存失败'))
    const wrapper = mount(NodeTable, {
      props: {
        groups,
        now: new Date('2026-07-26T02:00:00Z'),
        nodes: [{
          id: 'node-1', group_id: 'group-1', name: 'finance-pc', type: 'windows',
          agent_version: '0.3.3', os_name: 'Windows 11', os_version: '25H2', ip_address: '192.168.31.25',
          cpu_percent: 20.2, memory_percent: 55.6, disk_percent: 71.1,
          network_metrics_available: true, network_upload_mb_s: 1.25, network_download_mb_s: 8.75,
          last_seen_at: '2026-07-26T01:59:30Z', created_at: '2026-07-25T00:00:00Z',
        }],
      },
    })

    await wrapper.get('button[aria-label="编辑 finance-pc 的备注"]').trigger('click')
    await wrapper.get('form.remark-form').trigger('submit.prevent')
    await flushPromises()

    expect(wrapper.get('[role="alert"]').text()).toContain('保存失败')
    expect(wrapper.find('#node-remark').exists()).toBe(true)
    expect(wrapper.emitted('updated')).toBeUndefined()
  })

  it('hides stale live metrics when a node is offline', () => {
    const wrapper = mount(NodeTable, {
      props: {
        groups,
        now: new Date('2026-07-26T02:00:00Z'),
        nodes: [{
          id: 'node-1', group_id: 'group-1', name: 'archive-pc', type: 'windows',
          agent_version: '0.3.0', os_name: 'Windows 11', os_version: '23H2', ip_address: '10.0.0.8',
          cpu_percent: 20.2, memory_percent: 55.6, disk_percent: 71.1,
          network_metrics_available: true, network_upload_mb_s: 1.25, network_download_mb_s: 8.75,
          last_seen_at: '2026-07-26T01:50:00Z', created_at: '2026-07-25T00:00:00Z',
        }],
      },
    })

    expect(wrapper.get('td[data-label="CPU"]').text()).toBe('-')
    expect(wrapper.get('td[data-label="内存"]').text()).toBe('-')
    expect(wrapper.get('td[data-label="磁盘"]').text()).toBe('-')
    expect(wrapper.get('td[data-label="网络"]').text()).toBe('-')
  })

  it('shows the upgrade state when the node does not support network metrics', () => {
    const wrapper = mount(NodeTable, {
      props: {
        groups,
        now: new Date('2026-07-26T02:00:00Z'),
        nodes: [{
          id: 'node-1', group_id: 'group-1', name: 'legacy-pc', type: 'windows',
          agent_version: '0.0.9', os_name: 'Windows 11', os_version: '23H2', ip_address: '10.0.0.8',
          cpu_percent: 20.2, memory_percent: 55.6, disk_percent: 71.1,
          network_metrics_available: false, network_upload_mb_s: 0, network_download_mb_s: 0,
          last_seen_at: '2026-07-26T01:59:30Z', created_at: '2026-07-25T00:00:00Z',
        }],
      },
    })

    expect(wrapper.get('td[data-label="网络"]').text()).toContain('待升级')
  })

  it('renders the device group name instead of its internal ID', () => {
    const wrapper = mount(NodeTable, {
      props: {
        groups,
        now: new Date('2026-07-26T02:00:00Z'),
        nodes: [{
          id: 'node-1', group_id: 'group-1', name: 'finance-pc', type: 'windows',
          agent_version: '0.3.0', os_name: 'Windows 11', os_version: '23H2', ip_address: '10.0.0.8',
          cpu_percent: 20.2, memory_percent: 55.6, disk_percent: 71.1,
          network_metrics_available: false, network_upload_mb_s: 0, network_download_mb_s: 0,
          last_seen_at: '2026-07-26T01:59:30Z', created_at: '2026-07-25T00:00:00Z',
        }],
      },
    })

    expect(wrapper.get('td[data-label="分组"]').text()).toBe('办公电脑')
    expect(wrapper.get('td[data-label="分组"]').text()).not.toContain('group-1')
  })

  it('filters devices by group name', async () => {
    const wrapper = mount(NodeTable, {
      props: {
        groups,
        now: new Date('2026-07-26T02:00:00Z'),
        nodes: [
          {
            id: 'node-1', group_id: 'group-1', name: 'finance-pc', type: 'windows',
            agent_version: '0.3.0', os_name: 'Windows 11', os_version: '23H2', ip_address: '10.0.0.8',
            cpu_percent: 20.2, memory_percent: 55.6, disk_percent: 71.1,
            network_metrics_available: false, network_upload_mb_s: 0, network_download_mb_s: 0,
            last_seen_at: '2026-07-26T01:59:30Z', created_at: '2026-07-25T00:00:00Z',
          },
          {
            id: 'node-2', group_id: 'group-2', name: 'backup-server', type: 'linux',
            agent_version: '0.3.0', os_name: 'Ubuntu', os_version: '24.04', ip_address: '10.0.0.9',
            cpu_percent: 20.2, memory_percent: 55.6, disk_percent: 71.1,
            network_metrics_available: false, network_upload_mb_s: 0, network_download_mb_s: 0,
            last_seen_at: '2026-07-26T01:59:30Z', created_at: '2026-07-25T00:00:00Z',
          },
        ],
      },
    })

    await wrapper.get('input[type="search"]').setValue('生产服务器')

    expect(wrapper.text()).toContain('backup-server')
    expect(wrapper.text()).not.toContain('finance-pc')
  })
})
