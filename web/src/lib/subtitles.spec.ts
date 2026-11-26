import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  activeSubtitleLines,
  computeVideoContentBox,
  defaultSubtitleStyle,
  ensureFontFacesLoaded,
  loadSubtitleStyle,
  sanitizeSubtitleStyle,
  saveSubtitleStyle,
  subtitleOverlayStyles,
  subtitleQueryParams,
} from './subtitles'
import type { CueDTO } from '../api'

const sampleCues: CueDTO[] = [
  { idx: 0, start: 10, end: 13, text: 'Hello there' },
  { idx: 1, start: 14, end: 17, text: 'General Kenobi' },
]

function createMemoryStorage(): Storage {
  const store = new Map<string, string>()
  return {
    get length() {
      return store.size
    },
    clear: () => store.clear(),
    getItem: (key: string) => store.get(key) ?? null,
    key: (index: number) => Array.from(store.keys())[index] ?? null,
    removeItem: (key: string) => {
      store.delete(key)
    },
    setItem: (key: string, value: string) => {
      store.set(key, String(value))
    },
  }
}

describe('subtitles library', () => {
  beforeEach(() => {
    vi.stubGlobal('localStorage', createMemoryStorage())
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    document.getElementById('quipster-subtitle-fonts')?.remove()
  })

  it('sanitizes out-of-range values and persists to localStorage', () => {
    const clean = sanitizeSubtitleStyle({
      enabled: true,
      font: 'not-a-font',
      fontSize: 999,
      color: 'bad',
      outline: -5,
      outlineColor: '#12AB34',
      shadow: 10,
      bgOpacity: 2,
      bgColor: '00ff00',
      position: 'top',
      marginV: 500,
      bold: true,
      italic: false,
      uppercase: true,
    })
    expect(clean.font).toBe('inter')
    expect(clean.fontSize).toBe(96)
    expect(clean.color).toBe('#ffffff')
    expect(clean.outline).toBe(0)
    expect(clean.outlineColor).toBe('#12ab34')
    expect(clean.shadow).toBe(6)
    expect(clean.bgOpacity).toBe(1)
    expect(clean.bgColor).toBe('#00ff00')
    expect(clean.position).toBe('top')
    expect(clean.marginV).toBe(220)
    expect(clean.bold).toBe(true)
    expect(clean.uppercase).toBe(true)

    saveSubtitleStyle(clean)
    expect(loadSubtitleStyle()).toEqual(clean)
  })

  it('selects active cues and applies uppercase + start fallback', () => {
    expect(activeSubtitleLines(sampleCues, 11, false)).toEqual(['Hello there'])
    expect(activeSubtitleLines(sampleCues, 11, true)).toEqual(['HELLO THERE'])
    expect(activeSubtitleLines(sampleCues, 13.5, false, false)).toEqual([])
    expect(activeSubtitleLines(sampleCues, 9.8, true, true)).toEqual(['HELLO THERE'])
  })

  it('computes letterboxed and pillarboxed content rectangles', () => {
    // 2.40:1 cinema video inside 16:9 (640x360) container -> top/bottom letterboxing
    const scope = computeVideoContentBox(640, 360, 1920, 800)
    expect(scope.width).toBeCloseTo(640)
    expect(scope.height).toBeCloseTo(266.67, 1)
    expect(scope.left).toBe(0)
    expect(scope.top).toBeCloseTo(46.67, 1)

    // 4:3 episode inside 16:9 (640x360) container -> left/right pillarboxing
    const tv = computeVideoContentBox(640, 360, 4, 3)
    expect(tv.height).toBeCloseTo(360)
    expect(tv.width).toBeCloseTo(480)
    expect(tv.left).toBeCloseTo(80)
    expect(tv.top).toBe(0)
  })

  it('produces proportional CSS for outline and boxed modes', () => {
    const def = { ...defaultSubtitleStyle(), enabled: true, font: 'montserrat', fontSize: 40, bold: true }
    const outlineStyles = subtitleOverlayStyles(def, 360)
    expect(outlineStyles.text.fontSize).toBe('20.00px')
    expect(outlineStyles.text.fontWeight).toBe(700)
    expect(outlineStyles.text.paintOrder).toBe('stroke fill')
    expect(outlineStyles.text.backgroundColor).toBeUndefined()

    const boxed = { ...def, bgOpacity: 0.7, bgColor: '#112233', position: 'top' as const, marginV: 40 }
    const boxedStyles = subtitleOverlayStyles(boxed, 720)
    expect(boxedStyles.container.justifyContent).toBe('flex-start')
    expect(boxedStyles.container.paddingTop).toBe('40.0px')
    expect(boxedStyles.text.backgroundColor).toBe('rgba(17, 34, 51, 0.7)')
    expect(boxedStyles.text.WebkitTextStroke).toBeUndefined()
  })

  it('serializes query params only when enabled and injects @font-face once', () => {
    expect(subtitleQueryParams(defaultSubtitleStyle())).toEqual({})
    const params = subtitleQueryParams({
      ...defaultSubtitleStyle(),
      enabled: true,
      font: 'oswald',
      fontSize: 54,
      color: '#ffd166',
      bold: true,
      uppercase: true,
    })
    expect(params).toMatchObject({
      subs: '1',
      subFont: 'oswald',
      subSize: '54',
      subColor: 'ffd166',
      subBold: '1',
      subUpper: '1',
    })
    expect(params.subItalic).toBeUndefined()

    ensureFontFacesLoaded()
    ensureFontFacesLoaded()
    expect(document.querySelectorAll('#quipster-subtitle-fonts')).toHaveLength(1)
    expect(document.getElementById('quipster-subtitle-fonts')?.textContent).toContain('/api/fonts/montserrat-700.ttf')
  })
})
