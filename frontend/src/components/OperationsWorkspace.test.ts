import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { apiRequest } from '../api'
import type { NodeGroup } from '../types'
import OperationsWorkspace from './OperationsWorkspace.vue'

vi.mock('../api', () => ({
  apiRequest: vi.fn(),
}))

const ElDialogStub = defineComponent({
  name: 'ElDialog',
  props: {
    modelValue: { type: Boolean, default: false },
    title: { type: String, default: '' },
    width: { type: String, default: '' },
    alignCenter: { type: Boolean, default: false },
  },
  emits: ['update:modelValue'],
  template: `
    <div data-testid="el-dialog-stub" :data-open="modelValue">
      <slot />
    </div>
  `,
})

function mountWorkspace(groups: NodeGroup[] = []) {
  vi.stubGlobal('matchMedia', vi.fn(() => ({ matches: false })))
  return mount(OperationsWorkspace, {
    props: {
      owner: { id: 'owner-1', username: 'jarvis' },
      organizations: [],
      sites: [],
      groups,
      nodes: [],
    },
    global: {
      stubs: {
        ElDialog: ElDialogStub,
      },
    },
  })
}

describe('OperationsWorkspace', () => {
  afterEach(() => {
    vi.clearAllMocks()
    vi.unstubAllGlobals()
  })

  it('switches between overview and client downloads with accurate active navigation', async () => {
    const wrapper = mountWorkspace()
    const overview = wrapper.get('a[href="#nodes"]')
    const downloads = wrapper.get('a[href="#downloads"]')

    expect(overview.attributes('aria-current')).toBe('page')
    await downloads.trigger('click')

    expect(downloads.attributes('aria-current')).toBe('page')
    expect(wrapper.text()).toContain('选择客户端平台')
    expect(wrapper.find('.metric-band').exists()).toBe(false)

    await overview.trigger('click')
    expect(overview.attributes('aria-current')).toBe('page')
    expect(wrapper.find('.metric-band').exists()).toBe(true)
    wrapper.unmount()
  })

  it('closes the mobile navigation after selecting downloads', async () => {
    const wrapper = mountWorkspace()

    await wrapper.get('button[title="打开导航"]').trigger('click')
    expect(wrapper.get('aside').classes()).toContain('open')

    await wrapper.get('a[href="#downloads"]').trigger('click')
    expect(wrapper.get('aside').classes()).not.toContain('open')
    wrapper.unmount()
  })

  it('opens the existing enrollment dialog from downloads and shows commands for downloaded files', async () => {
    vi.mocked(apiRequest).mockResolvedValue({ token: 'enrollment-token' })
    const wrapper = mountWorkspace([
      { id: 'group-1', site_id: 'site-1', name: '生产环境', created_at: '2026-07-27T00:00:00Z' },
    ])

    await wrapper.get('a[href="#downloads"]').trigger('click')
    const enrollButton = wrapper.get('button[data-action="enroll"]')
    expect(enrollButton.attributes('disabled')).toBeUndefined()
    await enrollButton.trigger('click')

    expect(wrapper.get('[data-testid="el-dialog-stub"]').attributes('data-open')).toBe('true')

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    const commands = wrapper.findAll('.command-line code').map(command => command.text())
    expect(commands).toEqual([
      `.\\AceAgent-windows-amd64.exe -server ${window.location.origin} -enrollment enrollment-token`,
      `chmod +x ./ace-agent-linux-amd64 && ./ace-agent-linux-amd64 -server ${window.location.origin} -enrollment enrollment-token`,
    ])
    wrapper.unmount()
  })

  it('disables enrollment from downloads when no groups exist', async () => {
    const wrapper = mountWorkspace()

    await wrapper.get('a[href="#downloads"]').trigger('click')

    expect(wrapper.get('button[data-action="enroll"]').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })
})
