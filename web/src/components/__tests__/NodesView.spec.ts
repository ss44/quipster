import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import NodesView from '../NodesView.vue'
import { api, type NodeView } from '../../api'

vi.mock('../../api', () => ({ api: { nodes: vi.fn() } }))

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: Error) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

const node: NodeView = {
  node: { id: 1, name: 'Worker one', status: 'approved', version: '1', lastSeen: 0, jobsDone: 0, jobsFailed: 0, createdAt: 0 },
  online: false,
  currentJobs: [],
}

let wrapper: VueWrapper
let request: ReturnType<typeof deferred<{ nodes: NodeView[] }>>

beforeEach(() => {
  vi.useFakeTimers()
  request = deferred<{ nodes: NodeView[] }>()
  vi.mocked(api.nodes).mockReturnValue(request.promise)
  wrapper = mount(NodesView)
})

afterEach(() => {
  wrapper.unmount()
  vi.useRealTimers()
  vi.clearAllMocks()
})

describe('NodesView loading', () => {
  it('hides empty, offline and idle states behind initial text haze', () => {
    expect(wrapper.find('[role="status"][aria-label="Loading worker nodes"]').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('No nodes yet')
    expect(wrapper.text()).not.toContain('offline')
    expect(wrapper.text()).not.toContain('idle')
    expect(wrapper.findAll('tbody tr')).toHaveLength(0)
  })

  it('shows a genuine empty state once the initial request resolves', async () => {
    request.resolve({ nodes: [] })
    await flushPromises()
    expect(wrapper.find('.haze-text').exists()).toBe(false)
    expect(wrapper.text()).toContain('No nodes yet')
  })

  it('shows known zero, offline and idle values without placeholders', async () => {
    request.resolve({ nodes: [node] })
    await flushPromises()
    expect(wrapper.find('.haze-text').exists()).toBe(false)
    expect(wrapper.get('tbody').text()).toContain('Worker one')
    expect(wrapper.get('.state').text()).toBe('offline')
    expect(wrapper.get('tbody').text()).toContain('idle')
    expect(wrapper.findAll('tbody td').slice(3, 5).map((cell) => cell.text())).toEqual(['0', '0'])
    expect(wrapper.text()).not.toContain('No nodes yet')
  })

  it('settles initial errors and recovers on the next scheduled refresh', async () => {
    request.reject(new Error('Network error'))
    await flushPromises()
    expect(wrapper.find('.haze-text').exists()).toBe(false)
    expect(wrapper.get('[role="alert"]').text()).toContain('Retrying automatically')
    expect(wrapper.text()).not.toContain('No nodes yet')

    const retry = deferred<{ nodes: NodeView[] }>()
    vi.mocked(api.nodes).mockReturnValue(retry.promise)
    await vi.advanceTimersByTimeAsync(5000)
    expect(wrapper.find('.haze-text').exists()).toBe(false)
    expect(wrapper.find('[role="alert"]').exists()).toBe(true)
    retry.resolve({ nodes: [node] })
    await flushPromises()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.get('tbody').text()).toContain('Worker one')
  })

  it('keeps known rows during pending and failed refreshes', async () => {
    request.resolve({ nodes: [node] })
    await flushPromises()
    const refresh = deferred<{ nodes: NodeView[] }>()
    vi.mocked(api.nodes).mockReturnValue(refresh.promise)
    await vi.advanceTimersByTimeAsync(5000)
    expect(wrapper.find('.haze-text').exists()).toBe(false)
    expect(wrapper.get('tbody').text()).toContain('Worker one')

    refresh.reject(new Error('Network error'))
    await flushPromises()
    expect(wrapper.find('.haze-text').exists()).toBe(false)
    expect(wrapper.get('tbody').text()).toContain('Worker one')
    expect(wrapper.find('[role="alert"]').exists()).toBe(true)
  })

  it('cancels polling on unmount', async () => {
    wrapper.unmount()
    await vi.advanceTimersByTimeAsync(10000)
    expect(api.nodes).toHaveBeenCalledOnce()
  })
})
