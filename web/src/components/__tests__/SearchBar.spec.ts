import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'
import { createVuetify } from 'vuetify'
import * as components from 'vuetify/components'
import * as directives from 'vuetify/directives'
import SearchBar from '../SearchBar.vue'
import source from '../SearchBar.vue?raw'
import { api, type Suggestion } from '../../api'

vi.mock('../../api', () => ({ api: { suggest: vi.fn() } }))

const suggestMock = vi.mocked(api.suggest)
const vuetify = createVuetify({ components, directives })

const suggestions: Suggestion[] = [
  { text: 'hello there', textMarked: '<b>hello</b> there' },
  { text: 'help me', textMarked: '<b>hel</b>p me' },
]

let wrapper: VueWrapper | null = null

async function mountBar(): Promise<Router> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', name: 'home', component: { template: '<div />' } },
      { path: '/search', name: 'search', component: { template: '<div />' } },
    ],
  })
  await router.push('/')
  await router.isReady()
  wrapper = mount(SearchBar, {
    attachTo: document.body,
    global: { plugins: [vuetify, router], stubs: { transition: false } },
  })
  return router
}

function input(): ReturnType<VueWrapper['find']> {
  return wrapper!.find('input')
}

async function type(value: string) {
  await input().setValue(value)
}

// Types a suggestion-worthy query and lets the debounced suggest call resolve,
// which is how the panel opens in production.
async function openMenu() {
  suggestMock.mockResolvedValue({ suggestions })
  await type('he')
  await vi.advanceTimersByTimeAsync(150)
  await flushPromises()
}

// Vue drives <Transition> enter/leave completion through requestAnimationFrame
// (faked here), so settling timers plus microtasks yields the final DOM state.
async function settle() {
  await flushPromises()
  await vi.advanceTimersByTimeAsync(250)
}

beforeEach(() => {
  vi.useFakeTimers({
    toFake: [
      'setTimeout',
      'clearTimeout',
      'setInterval',
      'clearInterval',
      'setImmediate',
      'clearImmediate',
      'Date',
      'requestAnimationFrame',
      'cancelAnimationFrame',
    ],
  })
  suggestMock.mockReset()
})

afterEach(() => {
  wrapper?.unmount()
  wrapper = null
  document.body.innerHTML = ''
  vi.useRealTimers()
})

describe('SearchBar suggestion panel placement (issue #28)', () => {
  it('renders suggestions inline inside the search container instead of a teleported overlay', async () => {
    await mountBar()
    await openMenu()

    const wrapEl = wrapper!.find('.search-wrap').element
    const menu = wrapper!.find('.search-menu')
    expect(menu.exists()).toBe(true)
    expect(wrapEl.contains(menu.element)).toBe(true)
    // After the field in DOM order, so it can never stack above the input.
    expect(wrapEl.lastElementChild).toBe(menu.element)
    expect(wrapper!.findComponent({ name: 'VMenu' }).exists()).toBe(false)
    expect(document.querySelector('.v-overlay-container .search-menu')).toBeNull()
  })

  it('anchors the panel below the field with a gap under the documented z-index', () => {
    expect(source).not.toContain('<v-menu')
    expect(source).toMatch(/\.search-wrap\s*\{[^}]*?position:\s*relative/)
    expect(source).toMatch(/\.search-menu\s*\{[^}]*?position:\s*absolute/)
    expect(source).toMatch(/\.search-menu\s*\{[^}]*?top:\s*calc\(100% \+ 8px\)/)
    expect(source).toMatch(/\.search-menu\s*\{[^}]*?z-index:\s*60/)
  })
})

describe('SearchBar suggestion flow', () => {
  it('debounces input before requesting suggestions', async () => {
    await mountBar()
    suggestMock.mockResolvedValue({ suggestions })
    await type('he')
    expect(suggestMock).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(149)
    expect(suggestMock).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(1)
    await flushPromises()
    expect(suggestMock).toHaveBeenCalledTimes(1)
    expect(suggestMock).toHaveBeenCalledWith('he')
  })

  it('does not query or open the panel for queries shorter than two characters', async () => {
    await mountBar()
    await type('h')
    await vi.advanceTimersByTimeAsync(150)
    await flushPromises()
    expect(suggestMock).not.toHaveBeenCalled()
    expect(wrapper!.find('.search-menu').exists()).toBe(false)
  })

  it('shows the returned suggestions as options', async () => {
    await mountBar()
    await openMenu()
    const items = wrapper!.findAll('.menu-item')
    expect(items).toHaveLength(2)
    expect(items[0].text()).toContain('hello there')
  })

  it('closes the panel when the suggest request fails', async () => {
    await mountBar()
    suggestMock.mockRejectedValueOnce(new Error('suggest down'))
    await type('he')
    await vi.advanceTimersByTimeAsync(150)
    await flushPromises()
    expect(wrapper!.find('.search-menu').exists()).toBe(false)
  })
})

describe('SearchBar dismissing the panel', () => {
  it('closes on escape', async () => {
    await mountBar()
    await openMenu()
    await input().trigger('keydown', { key: 'Escape' })
    await settle()
    expect(wrapper!.find('.search-menu').exists()).toBe(false)
  })

  it('closes when the pointer goes down outside the search container', async () => {
    await mountBar()
    await openMenu()
    document.body.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    await settle()
    expect(wrapper!.find('.search-menu').exists()).toBe(false)
  })

  it('stays open for pointer downs inside the search container', async () => {
    await mountBar()
    await openMenu()
    const item = wrapper!.findAll('.menu-item')[0].element
    item.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    await flushPromises()
    expect(wrapper!.find('.search-menu').exists()).toBe(true)
  })

  it('reopens on refocus while suggestions are cached', async () => {
    await mountBar()
    await openMenu()
    await input().trigger('keydown', { key: 'Escape' })
    await settle()
    expect(wrapper!.find('.search-menu').exists()).toBe(false)
    ;(input().element as HTMLInputElement).focus()
    await flushPromises()
    expect(wrapper!.find('.search-menu').exists()).toBe(true)
  })
})

describe('SearchBar selection and navigation', () => {
  it('keeps taps in the field working: pressing a suggestion prevents default and navigates', async () => {
    const router = await mountBar()
    await openMenu()
    const item = wrapper!.findAll('.menu-item')[0].element
    const down = new MouseEvent('mousedown', { bubbles: true, cancelable: true })
    item.dispatchEvent(down)
    expect(down.defaultPrevented).toBe(true)
    await settle()
    expect(router.currentRoute.value.query.q).toBe('hello there')
    expect(wrapper!.find('.search-menu').exists()).toBe(false)
  })

  it('moves the active option with arrow keys and wraps around', async () => {
    await mountBar()
    await openMenu()
    await input().trigger('keydown', { key: 'ArrowDown' })
    expect(wrapper!.findAll('.menu-item')[1].classes()).toContain('hovered')
    await input().trigger('keydown', { key: 'ArrowDown' })
    expect(wrapper!.findAll('.menu-item')[0].classes()).toContain('hovered')
    await input().trigger('keydown', { key: 'ArrowUp' })
    expect(wrapper!.findAll('.menu-item')[1].classes()).toContain('hovered')
  })

  it('commits the active option on enter', async () => {
    const router = await mountBar()
    await openMenu()
    await input().trigger('keydown', { key: 'ArrowDown' })
    await input().trigger('keydown', { key: 'Enter' })
    await flushPromises()
    expect(router.currentRoute.value.query.q).toBe('help me')
  })

  it('submits the typed query when the panel is closed', async () => {
    const router = await mountBar()
    await type('wire')
    await input().trigger('keydown', { key: 'Enter' })
    await flushPromises()
    expect(router.currentRoute.value.query.q).toBe('wire')
    expect(wrapper!.find('.search-menu').exists()).toBe(false)
  })
})
