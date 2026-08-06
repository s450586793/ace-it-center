import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import AgentDownloads from './AgentDownloads.vue'

describe('AgentDownloads', () => {
  afterEach(() => { vi.restoreAllMocks() })

  it('shows the 0.3.8 Windows installer and no enrollment token guidance', async () => {
    vi.spyOn(globalThis, 'fetch').mockRejectedValue(new Error('manifest unavailable'))
    const wrapper = mount(AgentDownloads)
    await flushPromises()

    expect(wrapper.get('a[download="AceAgentSetup-windows-amd64-V0.3.8.exe"]').attributes('href'))
      .toBe('/downloads/windows/stable/AceAgentSetup-windows-amd64-V0.3.8.exe')
    expect(wrapper.text()).toContain('安装后从系统托盘打开 Ace Agent，填写服务器地址，再回到平台的待配对设备确认')
    expect(wrapper.text()).not.toContain('Enrollment Token')
    expect(wrapper.find('[data-action="enroll"]').exists()).toBe(false)
  })

  it('keeps the published fallback download when the release manifest is invalid', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({
      version: '0.3.3',
      url: '/downloads/windows/stable/untrusted-installer.exe',
    }), { status: 200 }))
    const wrapper = mount(AgentDownloads)
    await flushPromises()

    expect(wrapper.get('a[download="AceAgentSetup-windows-amd64-V0.3.8.exe"]').attributes('href'))
      .toBe('/downloads/windows/stable/AceAgentSetup-windows-amd64-V0.3.8.exe')
  })

  it('lists the Windows installer and Linux binary as accessible same-origin downloads', async () => {
    vi.spyOn(globalThis, 'fetch').mockRejectedValue(new Error('manifest unavailable'))
    const wrapper = mount(AgentDownloads)
    await flushPromises()

    const rows = wrapper.findAll('.agent-download-row')
    expect(rows).toHaveLength(2)
    expect(rows[0].text()).toContain('Windows 10/11 x64')
    expect(rows[1].text()).toContain('Linux x64')
    expect(wrapper.get('a[href="/downloads/ace-agent-linux-amd64"]').attributes('download')).toBe('ace-agent-linux-amd64')
    expect(wrapper.text()).not.toContain('-enrollment')
  })
})
