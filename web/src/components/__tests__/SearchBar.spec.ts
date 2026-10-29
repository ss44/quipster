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

async function mountBar(initialRoute = '/'): Promise<Router> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', name: 'home', component: { template: '<div />' } },
      { path: '/search', name: 'search', component: { template: '<div />' } },
    ],
  })
  await router.push(initialRoute)
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

// Typing requires a focused field in production, and the component only
// surfaces suggestions for a focused field (late replies after blur must not
// re-open the panel), so the helper focuses first.
async function type(value: string) {
  ;(input().element as HTMLInputElement).focus()
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

// The option list leads with a row for the literal query (index 0), so
// suggestions start at index 1.
function menuItems() {
  return wrapper!.findAll('.menu-item')
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

  it('shows the returned suggestions as options below the raw query row', async () => {
    await mountBar()
    await openMenu()
    const items = menuItems()
    expect(items).toHaveLength(3)
    expect(items[0].text()).toContain('Search for')
    expect(items[0].text()).toContain('he')
    expect(items[1].text()).toContain('hello there')
    expect(items[2].text()).toContain('help me')
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
  it('closes on escape while keeping the text and caret focus', async () => {
    await mountBar()
    await openMenu()
    await input().trigger('keydown', { key: 'Escape' })
    await settle()
    expect(wrapper!.find('.search-menu').exists()).toBe(false)
    expect((input().element as HTMLInputElement).value).toBe('he')
    expect(document.activeElement).toBe(input().element)
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
    const item = menuItems()[0].element
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
    // Escape keeps the caret in the field (issue #29), so refocusing means
    // leaving and returning: blur, then focus again.
    const el = input().element as HTMLInputElement
    el.blur()
    el.focus()
    await flushPromises()
    expect(wrapper!.find('.search-menu').exists()).toBe(true)
  })
})

describe('SearchBar selection and navigation', () => {
  it('keeps taps in the field working: activating a suggestion prevents default on press and navigates', async () => {
    const router = await mountBar()
    await openMenu()
    const item = menuItems()[1].element
    const down = new MouseEvent('mousedown', { bubbles: true, cancelable: true })
    item.dispatchEvent(down)
    expect(down.defaultPrevented).toBe(true)
    // Activation happens on click, not mousedown, so a keyboard-focused
    // option also activates with Enter (which fires click, not mousedown).
    item.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await settle()
    expect(router.currentRoute.value.query.q).toBe('hello there')
    expect(wrapper!.find('.search-menu').exists()).toBe(false)
  })

  it('moves the active option with arrow keys and wraps around', async () => {
    await mountBar()
    await openMenu()
    await input().trigger('keydown', { key: 'ArrowDown' })
    expect(menuItems()[1].classes()).toContain('active-row')
    await input().trigger('keydown', { key: 'ArrowDown' })
    expect(menuItems()[2].classes()).toContain('active-row')
    await input().trigger('keydown', { key: 'ArrowDown' })
    expect(menuItems()[0].classes()).toContain('active-row')
    await input().trigger('keydown', { key: 'ArrowUp' })
    expect(menuItems()[2].classes()).toContain('active-row')
  })

  it('commits the active option on enter after explicit keyboard navigation', async () => {
    const router = await mountBar()
    await openMenu()
    await input().trigger('keydown', { key: 'ArrowDown' })
    await input().trigger('keydown', { key: 'Enter' })
    await flushPromises()
    expect(router.currentRoute.value.query.q).toBe('hello there')
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

describe('SearchBar raw query submission (issue #29)', () => {
  it('submits the raw typed query on enter while the suggestion menu is open', async () => {
    const router = await mountBar()
    await openMenu()
    expect(wrapper!.find('.search-menu').exists()).toBe(true)

    await input().trigger('keydown', { key: 'Enter' })
    await settle()

    expect(router.currentRoute.value.query.q).toBe('he')
  })

  it('does not navigate when the query is empty', async () => {
    const router = await mountBar()
    await input().trigger('keydown', { key: 'Enter' })
    await settle()
    expect(router.currentRoute.value.query.q).toBeUndefined()
  })

  it('keeps the active search mode when submitting a raw query', async () => {
    const router = await mountBar('/?mode=semantic')
    await openMenu()
    await input().trigger('keydown', { key: 'Enter' })
    await settle()
    expect(router.currentRoute.value.query).toMatchObject({ q: 'he', mode: 'semantic' })
  })

  it('returns to the raw query row after arrowing past the last suggestion', async () => {
    const router = await mountBar()
    await openMenu()
    for (let i = 0; i < 3; i++) await input().trigger('keydown', { key: 'ArrowDown' })
    expect(menuItems()[0].classes()).toContain('active-row')
    await input().trigger('keydown', { key: 'Enter' })
    await settle()
    expect(router.currentRoute.value.query.q).toBe('he')
  })

  it('never lets a pointer hover move the keyboard selection', async () => {
    const router = await mountBar()
    await openMenu()
    menuItems()[2].element.dispatchEvent(new MouseEvent('mouseenter'))
    expect(menuItems()[0].classes()).toContain('active-row')
    await input().trigger('keydown', { key: 'Enter' })
    await settle()
    expect(router.currentRoute.value.query.q).toBe('he')
  })

  it('submits the raw query when the pinned row is clicked', async () => {
    const router = await mountBar()
    await openMenu()
    menuItems()[0].element.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await settle()
    expect(router.currentRoute.value.query.q).toBe('he')
  })

  it('re-opens a dismissed menu with the arrow keys without losing the query', async () => {
    const router = await mountBar()
    await openMenu()
    await input().trigger('keydown', { key: 'Escape' })
    await settle()
    await input().trigger('keydown', { key: 'ArrowDown' })
    await settle()
    expect(wrapper!.find('.search-menu').exists()).toBe(true)
    expect(menuItems()[0].classes()).toContain('active-row')
    await input().trigger('keydown', { key: 'Enter' })
    await settle()
    expect(router.currentRoute.value.query.q).toBe('he')
  })

  it('exposes combobox and listbox semantics while the menu is open', async () => {
    await mountBar()
    await openMenu()
    const el = input().element as HTMLInputElement
    expect(el.getAttribute('role')).toBe('combobox')
    expect(el.getAttribute('aria-expanded')).toBe('true')
    expect(el.getAttribute('aria-activedescendant')).toBe('search-opt-0')
    expect(wrapper!.find('[role="listbox"]').attributes('id')).toBe('search-suggestions')
    expect(wrapper!.findAll('[role="option"]')).toHaveLength(3)
  })

  it('ignores stale suggestion responses once the query moved on', async () => {
    await mountBar()
    suggestMock.mockImplementation((q: string) =>
      q === 'he'
        ? new Promise((resolve) => setTimeout(() => resolve({ suggestions }), 300))
        : Promise.resolve({ suggestions: [] }),
    )
    await type('he')
    await vi.advanceTimersByTimeAsync(150) // first request goes out
    await type('he extra') // query moved on; second request scheduled
    await vi.advanceTimersByTimeAsync(300) // second reply lands, stale first reply resolves
    await flushPromises()
    expect(suggestMock).toHaveBeenCalledTimes(2)
    // The stale reply carried suggestions, yet the panel stays closed.
    expect(wrapper!.find('.search-menu').exists()).toBe(false)
  })

  it('does not re-open the menu from a response after the field lost focus', async () => {
    await mountBar()
    suggestMock.mockResolvedValue({ suggestions })
    await type('he')
    ;(input().element as HTMLInputElement).blur()
    await vi.advanceTimersByTimeAsync(150)
    await flushPromises()
    expect(suggestMock).not.toHaveBeenCalled()
    expect(wrapper!.find('.search-menu').exists()).toBe(false)
  })
})
