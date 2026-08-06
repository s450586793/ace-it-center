import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { apiRequest } from '../api'
import type { CommandTask, CommandTaskDetail, Node } from '../types'
import CommandCenter from './CommandCenter.vue'

vi.mock('../api', () => ({ apiRequest: vi.fn() }))

const windowsOnline: Node = {
  id: 'node-1', group_id: 'group-1', name: 'finance-pc', type: 'windows', agent_version: '0.4.0',
  os_name: 'Windows 11', os_version: '23H2', ip_address: '192.168.1.10', cpu_percent: 10,
  memory_percent: 20, disk_percent: 30, network_metrics_available: true,
  network_upload_mb_s: 1, network_download_mb_s: 2,
  last_seen_at: new Date().toISOString(), created_at: '2026-08-03T00:00:00Z',
}

const windowsOffline: Node = {
  ...windowsOnline, id: 'node-2', name: 'warehouse-pc', last_seen_at: null,
}

const linuxNode: Node = {
  ...windowsOnline, id: 'linux-1', name: 'nas', type: 'linux', os_name: 'Linux',
}

const failedTask: CommandTask = {
  id: 'task-1', shell: 'powershell', command: 'hostname', timeout_seconds: 300,
  created_by: 'owner-1', created_at: '2026-08-03T12:00:00Z', target_count: 2,
  counts: { queued: 0, leased: 0, running: 0, succeeded: 1, failed: 1, timed_out: 0 },
}

function mountCommandCenter() {
  return mount(CommandCenter, {
    props: { nodes: [windowsOnline, windowsOffline, linuxNode] },
    global: {
      stubs: {
        ElDrawer: {
          props: ['modelValue'],
          template: '<section v-if="modelValue" data-testid="command-detail"><slot /></section>',
        },
      },
    },
  })
}

describe('CommandCenter', () => {
  beforeEach(() => {
    vi.mocked(apiRequest).mockResolvedValue({ items: [] })
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.mocked(apiRequest).mockReset()
  })

  it('lists only Windows devices and keeps offline devices selectable', async () => {
    const wrapper = mountCommandCenter()
    await flushPromises()

    expect(wrapper.find('[data-node-id="node-1"]').exists()).toBe(true)
    expect(wrapper.find('[data-node-id="node-2"]').exists()).toBe(true)
    expect(wrapper.find('[data-node-id="linux-1"]').exists()).toBe(false)
    expect(wrapper.get('[data-node-id="node-2"]').text()).toContain('离线')
    expect(wrapper.get<HTMLInputElement>('[data-node-id="node-2"] input').element.disabled).toBe(false)
    wrapper.unmount()
  })

  it('requires risk confirmation and submits the exact command request', async () => {
    vi.mocked(apiRequest).mockImplementation(async (path, options) => {
      if (path === '/api/v1/commands' && options?.method === 'POST') {
        return { task: { ...failedTask, id: 'task-new' }, executions: [] }
      }
      return { items: [] }
    })
    const wrapper = mountCommandCenter()
    await flushPromises()

    await wrapper.get<HTMLInputElement>('[data-node-id="node-1"] input').setValue(true)
    await wrapper.get<HTMLTextAreaElement>('#command-text').setValue('hostname')
    expect(wrapper.get<HTMLButtonElement>('[data-action="submit-command"]').element.disabled).toBe(true)
    await wrapper.get<HTMLInputElement>('#command-risk').setValue(true)
    await wrapper.get('form').trigger('submit.prevent')
    await flushPromises()

    expect(apiRequest).toHaveBeenCalledWith('/api/v1/commands', {
      method: 'POST',
      body: JSON.stringify({
        node_ids: ['node-1'], shell: 'powershell', command: 'hostname', timeout_seconds: 300,
      }),
    })
    expect(wrapper.get<HTMLInputElement>('[data-node-id="node-1"] input').element.checked).toBe(false)
    wrapper.unmount()
  })

  it('polls command history every five seconds only while mounted', async () => {
    vi.useFakeTimers()
    const wrapper = mountCommandCenter()
    await flushPromises()
    expect(apiRequest).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(5_000)
    await flushPromises()
    expect(apiRequest).toHaveBeenCalledTimes(2)

    wrapper.unmount()
    await vi.advanceTimersByTimeAsync(10_000)
    expect(apiRequest).toHaveBeenCalledTimes(2)
  })

  it('loads plain-text execution detail and retries failed targets', async () => {
    const detail: CommandTaskDetail = {
      task: failedTask,
      executions: [{
        id: 'execution-1', task_id: 'task-1', node_id: 'node-1', node_name: 'finance-pc',
        status: 'failed', attempt: 1, started_at: '2026-08-03T12:00:01Z',
        finished_at: '2026-08-03T12:00:02Z', exit_code: 7,
        output: '<script>unsafe()</script>', output_truncated: false,
        error_message: 'process exited with code 7', duration_ms: 1000,
      }],
    }
    vi.mocked(apiRequest).mockImplementation(async (path, options) => {
      if (path === '/api/v1/commands') return { items: [failedTask] }
      if (path === '/api/v1/commands/task-1' && !options) return detail
      if (path === '/api/v1/commands/task-1/retry') return { task: { ...failedTask, id: 'task-2' }, executions: [] }
      throw new Error(`unexpected request ${path}`)
    })
    const wrapper = mountCommandCenter()
    await flushPromises()

    await wrapper.get('[data-action="command-detail"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="command-detail"]').text()).toContain('<script>unsafe()</script>')
    expect(wrapper.find('[data-testid="command-detail"] script').exists()).toBe(false)

    await wrapper.get('[data-action="retry-command"]').trigger('click')
    await flushPromises()
    expect(apiRequest).toHaveBeenCalledWith('/api/v1/commands/task-1/retry', {
      method: 'POST', body: '{}',
    })
    wrapper.unmount()
  })
})
