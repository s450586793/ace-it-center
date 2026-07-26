import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import OperationsWorkspace from './OperationsWorkspace.vue'

function mountWorkspace() {
  vi.stubGlobal('matchMedia', vi.fn(() => ({ matches: false })))
  return mount(OperationsWorkspace, {
    props: {
      owner: { id: 'owner-1', username: 'jarvis' },
      organizations: [],
      sites: [],
      groups: [],
      nodes: [],
    },
  })
}

describe('OperationsWorkspace', () => {
  afterEach(() => vi.unstubAllGlobals())

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
})
