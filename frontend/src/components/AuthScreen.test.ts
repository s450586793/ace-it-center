import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import AuthScreen from './AuthScreen.vue'

describe('AuthScreen', () => {
  it('submits trimmed owner credentials during setup', async () => {
    const wrapper = mount(AuthScreen, { props: { mode: 'setup', busy: false, error: '' } })

    await wrapper.get('input[name="username"]').setValue('  jarvis  ')
    await wrapper.get('input[name="password"]').setValue('correct-horse-battery-staple')
    await wrapper.get('form').trigger('submit')

    expect(wrapper.emitted('submit')?.[0]).toEqual([{
      username: 'jarvis',
      password: 'correct-horse-battery-staple',
    }])
  })
})
