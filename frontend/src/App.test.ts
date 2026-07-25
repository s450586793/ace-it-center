import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import App from './App.vue'

describe('App', () => {
  afterEach(() => vi.restoreAllMocks())

  it('shows owner setup when the server is not initialized', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({ setup: false }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))

    const wrapper = mount(App)
    await flushPromises()

    expect(wrapper.text()).toContain('初始化 Owner')
    expect(wrapper.find('input[name="username"]').exists()).toBe(true)
  })
})
