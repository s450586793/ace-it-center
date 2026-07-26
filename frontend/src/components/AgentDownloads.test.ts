import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import AgentDownloads from './AgentDownloads.vue'

describe('AgentDownloads', () => {
  it('lists the supported platforms and exact same-origin downloads', () => {
    const wrapper = mount(AgentDownloads, { props: { canEnroll: true } })

    expect(wrapper.text()).toContain('Windows x64')
    expect(wrapper.text()).toContain('Linux x64')
    expect(wrapper.text()).toContain('Ace Agent')
    expect(wrapper.text()).toContain('MeshCentral Agent')

    const windows = wrapper.get('a[href="/downloads/AceAgent-windows-amd64.exe"]')
    const linux = wrapper.get('a[href="/downloads/ace-agent-linux-amd64"]')
    expect(windows.attributes('download')).toBe('AceAgent-windows-amd64.exe')
    expect(linux.attributes('download')).toBe('ace-agent-linux-amd64')
  })

  it('requests enrollment only when a target group is available', async () => {
    const wrapper = mount(AgentDownloads, { props: { canEnroll: true } })
    const enrollButton = wrapper.get('button[data-action="enroll"]')

    await enrollButton.trigger('click')
    expect(wrapper.emitted('enroll')).toHaveLength(1)

    await wrapper.setProps({ canEnroll: false })
    expect(enrollButton.attributes('disabled')).toBeDefined()
  })
})
