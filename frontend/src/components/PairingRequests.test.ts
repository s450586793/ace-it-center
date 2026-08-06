import { flushPromises, mount } from '@vue/test-utils'
import { ElMessageBox } from 'element-plus'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { apiRequest } from '../api'
import type { NodeGroup, PairingRequest } from '../types'
import PairingRequests from './PairingRequests.vue'

vi.mock('../api', () => ({ apiRequest: vi.fn() }))

const groups: NodeGroup[] = [
  { id: 'group-1', site_id: 'site-1', name: '办公电脑', created_at: '2026-07-28T00:00:00Z' },
  { id: 'group-2', site_id: 'site-1', name: '生产服务器', created_at: '2026-07-28T00:00:00Z' },
]

const pendingPairing: PairingRequest = {
  id: 'pairing-1', machine_id: 'machine-1', hostname: 'finance-pc', type: 'windows',
  agent_version: '0.3.2', state: 'pending', created_at: '2026-07-30T00:00:00Z', expires_at: '2026-07-30T01:00:00Z',
}

const recoveryPairing: PairingRequest = {
  ...pendingPairing,
  existing_node: { id: 'node-1', group_id: 'group-1', name: 'finance-pc', type: 'windows', agent_version: '0.3.1', os_name: 'Windows 11', os_version: '23H2', ip_address: '10.0.0.8', cpu_percent: 0, memory_percent: 0, disk_percent: 0, network_metrics_available: false, network_upload_mb_s: 0, network_download_mb_s: 0, last_seen_at: null, created_at: '2026-07-29T00:00:00Z' },
}

describe('PairingRequests', () => {
  afterEach(() => { vi.clearAllMocks() })

  it('requires a group before approving a pending pairing request', async () => {
    const wrapper = mount(PairingRequests, { props: { pairings: [pendingPairing], groups: [] } })
    await wrapper.get('button[data-action="approve-pairing-1"]').trigger('click')

    expect(wrapper.get('[role="alert"]').text()).toContain('请先创建并选择设备分组')
    expect(apiRequest).not.toHaveBeenCalled()
  })

  it('requests group creation from the pending pairings toolbar', async () => {
    const wrapper = mount(PairingRequests, { props: { pairings: [], groups } })

    await wrapper.get('[data-action="create-group"]').trigger('click')

    expect(wrapper.emitted('create-group')).toHaveLength(1)
  })

  it('approves a recovery request with the selected group and remark', async () => {
    vi.mocked(apiRequest).mockResolvedValue({ node: { id: 'node-1' }, pairing: { id: 'pairing-1', state: 'approved' } })
    const wrapper = mount(PairingRequests, { props: { pairings: [recoveryPairing], groups } })

    expect(wrapper.get<HTMLSelectElement>('select[name="group-pairing-1"]').element.value).toBe('group-1')
    await wrapper.get('select[name="group-pairing-1"]').setValue('group-2')
    await wrapper.get<HTMLTextAreaElement>('textarea[name="remark-pairing-1"]').setValue('15 楼财务电脑')
    await wrapper.get('button[data-action="approve-pairing-1"]').trigger('click')
    await flushPromises()

    expect(apiRequest).toHaveBeenCalledWith('/api/v1/pairings/pairing-1/approve', {
      method: 'POST', body: JSON.stringify({ group_id: 'group-2', remark: '15 楼财务电脑' }),
    })
    expect(wrapper.emitted('approved')).toHaveLength(1)
  })

  it('confirms before rejecting a pairing request', async () => {
    vi.spyOn(ElMessageBox, 'confirm').mockResolvedValue({ value: '', action: 'confirm' } as never)
    vi.mocked(apiRequest).mockResolvedValue({ pairing: { id: 'pairing-1', state: 'rejected' } })
    const wrapper = mount(PairingRequests, { props: { pairings: [pendingPairing], groups } })
    await wrapper.get('button[data-action="reject-pairing-1"]').trigger('click')
    await flushPromises()

    expect(ElMessageBox.confirm).toHaveBeenCalled()
    expect(apiRequest).toHaveBeenCalledWith('/api/v1/pairings/pairing-1/reject', { method: 'POST' })
    expect(wrapper.emitted('rejected')).toHaveLength(1)
  })
})
