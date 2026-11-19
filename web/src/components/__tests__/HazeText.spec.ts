import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import HazeText from '../HazeText.vue'

describe('HazeText', () => {
  it('exposes a loading status without reading fictional placeholder content', () => {
    const wrapper = mount(HazeText, {
      props: { label: 'Loading library total', text: '123,456' },
    })
    expect(wrapper.attributes('role')).toBe('status')
    expect(wrapper.attributes('aria-label')).toBe('Loading library total')
    expect(wrapper.get('.haze-text__ink').attributes('aria-hidden')).toBe('true')
    expect(wrapper.get('.haze-text__ink').text()).toBe('123,456')
    expect(wrapper.find('button, a, input, [tabindex]').exists()).toBe(false)
  })
})
