import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { apiRequest } from '../api'
import type { Node, NodeGroup, PairingRequest } from '../types'
import NodeTable from './NodeTable.vue'
import OperationsWorkspace from './OperationsWorkspace.vue'

vi.mock('../api', () => ({ apiRequest: vi.fn() }))

const SystemUpdateStub = defineComponent({
  emits: ['session-expired'],
  template: '<section data-testid="system-update">系统升级内容</section>',
})

const node: Node = {
  id: 'node-1', group_id: 'group-1', name: 'finance-pc', type: 'windows',
  agent_version: '0.3.2', os_name: 'Windows 11', os_version: '23H2', ip_address: '10.0.0.8',
  cpu_percent: 20.2, memory_percent: 55.6, disk_percent: 71.1,
  network_metrics_available: true, network_upload_mb_s: 1.25, network_download_mb_s: 8.75,
  last_seen_at: '2026-07-28T11:59:30Z', created_at: '2026-07-25T00:00:00Z',
}

const pendingPairing: PairingRequest = {
  id: 'pairing-1', machine_id: 'machine-1', hostname: 'finance-pc', type: 'windows',
  agent_version: '0.3.2', state: 'pending', created_at: '2026-07-30T00:00:00Z', expires_at: '2026-07-30T01:00:00Z',
}

function mountWorkspace(pairings: PairingRequest[] = []) {
  vi.stubGlobal('matchMedia', vi.fn(() => ({ matches: false })))
  if (!vi.isMockFunction(globalThis.fetch)) {
    vi.spyOn(globalThis, 'fetch').mockRejectedValue(new Error('manifest unavailable'))
  }
  return mount(OperationsWorkspace, {
    props: {
      owner: { id: 'owner-1', username: 'jarvis' },
      groups: [{ id: 'group-1', site_id: 'site-1', name: '办公电脑', created_at: '2026-07-28T00:00:00Z' }] as NodeGroup[],
      nodes: [node], pairings,
    },
    global: {
      stubs: {
        AgentDownloads: { template: '<section>选择客户端平台</section>' },
        SystemUpdate: SystemUpdateStub,
        ElDialog: { template: '<div><slot /></div>' },
      },
    },
  })
}

describe('OperationsWorkspace', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
    vi.mocked(apiRequest).mockReset()
  })

  it('shows the latest published Windows Agent version in the sidebar', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({
      version: '0.4.1',
      url: '/downloads/windows/stable/AceAgentSetup-windows-amd64-V0.4.1.exe',
    }), { status: 200 }))
    const wrapper = mountWorkspace()
    await flushPromises()

    expect(fetch).toHaveBeenCalledWith('/downloads/windows/stable/latest.json', { credentials: 'same-origin' })
    expect(wrapper.get('.sidebar-version').text()).toBe('ACE IT CENTER / V0.4.1')
    wrapper.unmount()
  })

  it('keeps the current stable version in the sidebar when the release manifest is unavailable', async () => {
    vi.spyOn(globalThis, 'fetch').mockRejectedValue(new Error('manifest unavailable'))
    const wrapper = mountWorkspace()
    await flushPromises()

    expect(wrapper.get('.sidebar-version').text()).toBe('ACE IT CENTER / V0.4.0')
    wrapper.unmount()
  })

  it('keeps overview and downloads navigation active states', async () => {
    const wrapper = mountWorkspace()
    const overview = wrapper.get('a[href="#nodes"]')
    const downloads = wrapper.get('a[href="#downloads"]')

    expect(overview.attributes('aria-current')).toBe('page')
    await downloads.trigger('click')
    expect(downloads.attributes('aria-current')).toBe('page')
    expect(wrapper.get('h1').text()).toBe('客户端下载')

    await overview.trigger('click')
    expect(overview.attributes('aria-current')).toBe('page')
    expect(wrapper.find('.metric-band').exists()).toBe(true)
    wrapper.unmount()
  })

  it('closes the mobile navigation after selecting downloads or network monitoring', async () => {
    const wrapper = mountWorkspace()

    await wrapper.get('button[title="打开导航"]').trigger('click')
    expect(wrapper.get('aside').classes()).toContain('open')
    await wrapper.get('a[href="#downloads"]').trigger('click')
    expect(wrapper.get('aside').classes()).not.toContain('open')

    await wrapper.get('button[title="打开导航"]').trigger('click')
    await wrapper.get('a[href="#network"]').trigger('click')
    expect(wrapper.get('aside').classes()).not.toContain('open')
    wrapper.unmount()
  })

  it('refreshes workspace data from the network view without a summary request', async () => {
    const wrapper = mountWorkspace()
    await wrapper.get('a[href="#network"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('h1').text()).toBe('网络监控')
    expect(apiRequest).not.toHaveBeenCalled()
    await wrapper.get('button[title="刷新数据"]').trigger('click')
    await flushPromises()

    expect(wrapper.emitted('refresh')).toHaveLength(1)
    expect(apiRequest).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('keeps group cards and group navigation out of the overview', () => {
    const wrapper = mountWorkspace([pendingPairing])

    expect(wrapper.find('#structure').exists()).toBe(false)
    expect(wrapper.find('a[href="#structure"]').exists()).toBe(false)
    expect(wrapper.find('.organization-block').exists()).toBe(false)
    expect(wrapper.get('.metric-band').text()).toContain('待配对')
    expect(wrapper.get('td[data-label="分组"]').text()).toBe('办公电脑')
    wrapper.unmount()
  })

  it('refreshes workspace data after a device remark is updated', async () => {
    const wrapper = mountWorkspace()

    wrapper.getComponent(NodeTable).vm.$emit('updated')
    await wrapper.vm.$nextTick()

    expect(wrapper.emitted('refresh')).toHaveLength(1)
    wrapper.unmount()
  })

  it('creates a flat group from the pending pairings view', async () => {
    vi.mocked(apiRequest).mockResolvedValue({ id: 'group-2', name: '财务电脑' })
    const wrapper = mountWorkspace()

    await wrapper.get('a[href="#pairings"]').trigger('click')
    expect(wrapper.get('[data-action="create-group"]').text()).toContain('新建分组')

    await wrapper.get('[data-action="create-group"]').trigger('click')
    expect(wrapper.find('#group-site').exists()).toBe(false)
    await wrapper.get<HTMLInputElement>('#group-name').setValue('财务电脑')
    await wrapper.get('form').trigger('submit.prevent')
    await flushPromises()

    expect(apiRequest).toHaveBeenCalledWith('/api/v1/groups', {
      method: 'POST', body: JSON.stringify({ name: '财务电脑' }),
    })
    expect(wrapper.emitted('refresh')).toHaveLength(1)
    wrapper.unmount()
  })

  it('opens pending pairings from the sidebar with a pending count', async () => {
    const wrapper = mountWorkspace([pendingPairing, { ...pendingPairing, id: 'approved-1', state: 'approved' }])
    const pairings = wrapper.get('a[href="#pairings"]')

    expect(pairings.text()).toContain('待配对设备')
    expect(pairings.get('b').text()).toBe('1')
    await pairings.trigger('click')

    expect(pairings.attributes('aria-current')).toBe('page')
    expect(wrapper.get('.section-index').text()).toBe('INFRASTRUCTURE / PAIRINGS')
    expect(wrapper.get('h1').text()).toBe('待配对设备')
    expect(wrapper.text()).toContain('finance-pc')
    wrapper.unmount()
  })

  it('opens pending pairings when adding a device', async () => {
    const wrapper = mountWorkspace([pendingPairing])
    await wrapper.get('button.primary-button.compact').trigger('click')
    await flushPromises()

    expect(wrapper.get('a[href="#pairings"]').attributes('aria-current')).toBe('page')
    expect(wrapper.find('[data-action="enroll"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('Enrollment Token')
    wrapper.unmount()
  })

  it('keeps client downloads available without enrollment controls', async () => {
    const wrapper = mountWorkspace()
    await wrapper.get('a[href="#downloads"]').trigger('click')

    expect(wrapper.get('h1').text()).toBe('客户端下载')
    expect(wrapper.find('[data-action="enroll"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('opens system updates from #updates and closes the mobile navigation', async () => {
    const wrapper = mountWorkspace()

    await wrapper.get('button[title="打开导航"]').trigger('click')
    expect(wrapper.get('aside').classes()).toContain('open')
    await wrapper.get('a[href="#updates"]').trigger('click')

    expect(wrapper.get('a[href="#updates"]').attributes('aria-current')).toBe('page')
    expect(wrapper.get('.section-index').text()).toBe('INFRASTRUCTURE / UPDATES')
    expect(wrapper.get('h1').text()).toBe('系统升级')
    expect(wrapper.get('[data-testid="system-update"]').text()).toContain('系统升级内容')
    expect(wrapper.get('aside').classes()).not.toContain('open')
    wrapper.unmount()
  })

  it('forwards a system update session expiry to the App boundary', async () => {
    const wrapper = mountWorkspace()
    await wrapper.get('a[href="#updates"]').trigger('click')
    const update = wrapper.getComponent(SystemUpdateStub)

    update.vm.$emit('session-expired')
    await wrapper.vm.$nextTick()

    expect(wrapper.emitted('session-expired')).toHaveLength(1)
    wrapper.unmount()
  })
})
