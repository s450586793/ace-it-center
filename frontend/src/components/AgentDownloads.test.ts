import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import AgentDownloads from './AgentDownloads.vue'

describe('AgentDownloads', () => {
  it('lists exactly two supported platforms with accessible same-origin downloads', () => {
    const wrapper = mount(AgentDownloads, { props: { canEnroll: true } })

    const rows = wrapper.findAll('.agent-download-row')
    expect(rows).toHaveLength(2)
    expect(rows[0].text()).toContain('Windows x64')
    expect(rows[1].text()).toContain('Linux x64')

    const windows = wrapper.get('a[href="/downloads/AceAgent-windows-amd64.exe"]')
    const linux = wrapper.get('a[href="/downloads/ace-agent-linux-amd64"]')
    expect(windows.attributes('download')).toBe('AceAgent-windows-amd64.exe')
    expect(linux.attributes('download')).toBe('ace-agent-linux-amd64')
    expect(windows.attributes('aria-label')).toBe('下载 Windows x64 Ace Agent：AceAgent-windows-amd64.exe')
    expect(linux.attributes('aria-label')).toBe('下载 Linux x64 Ace Agent：ace-agent-linux-amd64')

    expect(wrapper.text()).toContain('Ace Agent设备注册、基础资产采集、资源状态和心跳上报。')
    expect(wrapper.text()).toContain('MeshCentral Agent远程桌面、终端和文件操作使用的独立客户端，后续接入。')
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
