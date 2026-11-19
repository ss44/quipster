import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { createVuetify } from 'vuetify'
import { VBtn, VCheckbox, VIcon } from 'vuetify/components'
import type { DiskMetric, GaugeSnapshot, Metric } from '../../api'
import CachePanel from '../diagnostics/CachePanel.vue'
import HardwarePanel from '../diagnostics/HardwarePanel.vue'
import HintsPanel from '../diagnostics/HintsPanel.vue'
import OptimizationPanel from '../diagnostics/OptimizationPanel.vue'
import MetricGauge from '../diagnostics/MetricGauge.vue'
import type { DiagnosticHint } from '../../lib/diagnostics'

function metric(value: number, extra: Partial<Metric> = {}): Metric {
  return { updatedAt: 0, value, available: true, ...extra }
}

function disk(extra: Partial<DiskMetric> = {}): DiskMetric {
  return {
    path: '/data',
    name: 'data',
    freeBytes: 0,
    totalBytes: 0,
    available: true,
    ...extra,
  }
}

function gauge(extra: Partial<GaugeSnapshot> = {}): GaugeSnapshot {
  return {
    at: 0,
    cacheParts: {},
    disks: {},
    cpu: metric(5),
    processCpu: metric(5),
    ram: metric(5),
    swap: metric(0),
    load: { one: 0, five: 0, fifteen: 0, available: false },
    gpu: metric(0),
    vram: metric(0),
    hardware: {
      requested: 'auto',
      backend: 'none',
      device: '',
      encoder: 'libx264',
      decoder: '',
      available: false,
      reason: '',
      stats: {
        lastFallbackNote: '',
        hwEncodes: 0,
        cpuRenders: 0,
        fallbacks: 0,
        lastFallbackAt: 0,
      },
    },
    cacheBytes: 0,
    ...extra,
  }
}

const vuetify = createVuetify({ components: { VBtn, VCheckbox, VIcon } })

describe('HardwarePanel', () => {
  it('renders the active pipeline and emits action events', async () => {
    const wrapper = mount(HardwarePanel, {
      props: {
        hardware: gauge().hardware,
        gpu: null,
        system: null,
        exporting: false,
        copying: false,
        probing: false,
      },
      global: { plugins: [vuetify] },
    })

    expect(wrapper.text()).toContain('Hardware acceleration')
    expect(wrapper.text()).toContain('active pipeline')
    expect(wrapper.text()).toContain('SOFTWARE · CPU')

    const buttons = wrapper.findAll('button')
    await buttons[0].trigger('click')
    await buttons[3].trigger('click')
    expect(wrapper.emitted('export')).toHaveLength(1)
    expect(wrapper.emitted('reprobe')).toHaveLength(1)
  })

  it('keeps the include-paths option in sync with the view', async () => {
    const wrapper = mount(HardwarePanel, {
      props: {
        hardware: gauge().hardware,
        gpu: null,
        system: null,
        exporting: false,
        copying: false,
        probing: false,
      },
      global: { plugins: [vuetify] },
    })

    await wrapper.findComponent(VCheckbox).setValue(true)
    expect(wrapper.emitted('update:includePaths')?.[0]).toEqual([true])
  })
})

describe('CachePanel', () => {
  it('renders disk usage, cache parts and load average', () => {
    const wrapper = mount(CachePanel, {
      props: {
        gauge: gauge({
          cacheBytes: 4096,
          cacheParts: { clips: 1024 },
          disks: {
            data: disk({ name: 'data', path: '/data', totalBytes: 100, freeBytes: 25 }),
          },
          load: { one: 1.23, five: 0.5, fifteen: 0.25, available: true },
        }),
      },
    })

    expect(wrapper.text()).toContain('data')
    expect(wrapper.text()).toContain('25 B free')
    expect(wrapper.text()).toContain('clips 1.0 KB')
    expect(wrapper.text()).toContain('load 1.23 0.50 0.25')
    expect(wrapper.find('.bar .fill').attributes('style')).toContain('width: 75%')
  })

  it('distinguishes pending storage, unavailable storage and an empty sample', async () => {
    const wrapper = mount(CachePanel, { props: { gauge: null, pending: true } })
    expect(wrapper.find('[aria-label="Loading storage"]').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('0 B cached')
    expect(wrapper.text()).not.toContain('load n/a')

    await wrapper.setProps({ pending: false })
    expect(wrapper.find('[role="status"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('Storage unavailable.')
    expect(wrapper.text()).toContain('load n/a')

    await wrapper.setProps({ gauge: gauge() })
    expect(wrapper.text()).toContain('No storage details reported.')
    expect(wrapper.text()).toContain('No cache entries.')
    expect(wrapper.text()).toContain('0 B cached')
    expect(wrapper.text()).not.toContain('Waiting')
  })
})

describe('OptimizationPanel', () => {
  it('renders only known, positively counted optimization findings', () => {
    const wrapper = mount(OptimizationPanel, {
      props: {
        optimization: {
          updatedAt: 0,
          scanned: 10,
          recommended: 2,
          codes: {
            video_codec_compatibility: 2,
            high_video_bitrate: 0,
            unknown_code: 4,
          },
        },
      },
    })

    expect(wrapper.text()).toContain('Video codec compatibility')
    expect(wrapper.text()).toContain('2 files')
    expect(wrapper.text()).toContain('H.264 derivative')
    expect(wrapper.text()).not.toContain('High video bitrate')
    expect(wrapper.text()).not.toContain('unknown_code')
  })

  it('distinguishes pending, unavailable and empty findings', async () => {
    const wrapper = mount(OptimizationPanel, { props: { optimization: null, pending: true } })
    expect(wrapper.find('[aria-label="Loading optimization findings"]').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('0 of 0')
    expect(wrapper.text()).not.toContain('No probed media')

    await wrapper.setProps({ pending: false })
    expect(wrapper.find('[role="status"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('Optimization checks unavailable.')
    expect(wrapper.text()).not.toContain('0 of 0')

    await wrapper.setProps({ optimization: { updatedAt: 0, scanned: 0, recommended: 0, codes: {} } })
    expect(wrapper.text()).toContain('No probed media findings yet.')
    expect(wrapper.text()).toContain('0 of 0 probed files flagged')
  })
})

describe('MetricGauge', () => {
  it('uses haze only while pending, not for null or unavailable metrics after a snapshot', async () => {
    const wrapper = mount(MetricGauge, { props: { label: 'CPU', metric: null, pending: true } })
    expect(wrapper.find('[aria-label="Loading CPU"]').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('n/a')

    await wrapper.setProps({ pending: false })
    expect(wrapper.find('[role="status"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('n/a')
    expect(wrapper.text()).toContain('not available')
    expect(wrapper.text()).not.toContain('waiting')

    await wrapper.setProps({ metric: metric(0, { available: false, reason: 'Probe unavailable' }) })
    expect(wrapper.text()).toContain('n/a')
    expect(wrapper.text()).toContain('Probe unavailable')

    await wrapper.setProps({ metric: metric(0, { unit: 'percent' }) })
    expect(wrapper.text()).toContain('0%')
    expect(wrapper.text()).not.toContain('n/a')
  })
})

describe('HintsPanel', () => {
  it('renders hints with their severity tone', () => {
    const hints: DiagnosticHint[] = [
      { tone: 'bad', text: 'Forced backend failed' },
      { tone: 'warn', text: 'Cache nearly full' },
    ]
    const wrapper = mount(HintsPanel, {
      props: { hints },
      global: { stubs: { 'v-icon': true } },
    })

    const items = wrapper.findAll('.hint')
    expect(items).toHaveLength(2)
    expect(items[0].classes()).toContain('bad')
    expect(items[0].text()).toContain('Forced backend failed')
    expect(items[1].classes()).toContain('warn')
  })
})
