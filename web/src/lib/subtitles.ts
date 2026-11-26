import type { CSSProperties } from 'vue'
import type { CueDTO } from '../api'

export type SubtitlePosition = 'bottom' | 'middle' | 'top'

export interface SubtitleFont {
  id: string
  family: string
  category: 'sans-serif' | 'serif' | 'display' | 'monospace'
  hasBold: boolean
}

export interface SubtitleStyle {
  enabled: boolean
  font: string
  fontSize: number
  color: string
  outline: number
  outlineColor: string
  shadow: number
  bgOpacity: number
  bgColor: string
  position: SubtitlePosition
  marginV: number
  bold: boolean
  italic: boolean
  uppercase: boolean
}

export interface SubtitlePreset {
  id: string
  label: string
  patch: Partial<SubtitleStyle>
}

export interface VideoContentBox {
  left: number
  top: number
  width: number
  height: number
}

/** Virtual ASS PlayResY height shared between the browser overlay and libass. */
export const SUBTITLE_PLAY_RES_Y = 720
const ASS_MARGIN_H = 48
const ASS_BOX_PADDING = 10
const STORAGE_KEY = 'quipster.subtitleStyle.v1'
const FONT_STYLE_TAG_ID = 'quipster-subtitle-fonts'

/**
 * Curated open-source Google Fonts bundled in internal/fonts so browser
 * preview and FFmpeg burn-in render the exact same TrueType faces.
 */
export const SUBTITLE_FONTS: readonly SubtitleFont[] = [
  { id: 'inter', family: 'Inter', category: 'sans-serif', hasBold: true },
  { id: 'montserrat', family: 'Montserrat', category: 'sans-serif', hasBold: true },
  { id: 'oswald', family: 'Oswald', category: 'display', hasBold: true },
  { id: 'roboto', family: 'Roboto', category: 'sans-serif', hasBold: true },
  { id: 'merriweather', family: 'Merriweather', category: 'serif', hasBold: true },
  { id: 'jetbrains-mono', family: 'JetBrains Mono', category: 'monospace', hasBold: true },
  { id: 'bangers', family: 'Bangers', category: 'display', hasBold: false },
]

export const TEXT_COLOR_SWATCHES = [
  { hex: '#ffffff', label: 'White' },
  { hex: '#ffd166', label: 'Cinema yellow' },
  { hex: '#7dd3fc', label: 'Sky' },
  { hex: '#86efac', label: 'Mint' },
  { hex: '#fda4af', label: 'Coral' },
  { hex: '#c4b5fd', label: 'Lavender' },
] as const

export const OUTLINE_COLOR_SWATCHES = [
  { hex: '#000000', label: 'Black' },
  { hex: '#18181b', label: 'Charcoal' },
  { hex: '#1e3a8a', label: 'Navy' },
  { hex: '#7f1d1d', label: 'Crimson' },
  { hex: '#ffffff', label: 'White' },
] as const

export const SUBTITLE_PRESETS: readonly SubtitlePreset[] = [
  {
    id: 'classic',
    label: 'Classic',
    patch: {
      font: 'inter',
      fontSize: 40,
      color: '#ffffff',
      outline: 2.5,
      outlineColor: '#000000',
      shadow: 1.5,
      bgOpacity: 0,
      bgColor: '#000000',
      position: 'bottom',
      marginV: 36,
      bold: false,
      italic: false,
      uppercase: false,
    },
  },
  {
    id: 'cinema',
    label: 'Cinema',
    patch: {
      font: 'inter',
      fontSize: 42,
      color: '#ffd166',
      outline: 2.5,
      outlineColor: '#000000',
      shadow: 1.5,
      bgOpacity: 0,
      bgColor: '#000000',
      position: 'bottom',
      marginV: 36,
      bold: false,
      italic: false,
      uppercase: false,
    },
  },
  {
    id: 'social',
    label: 'Bold Social',
    patch: {
      font: 'montserrat',
      fontSize: 48,
      color: '#ffffff',
      outline: 3.5,
      outlineColor: '#000000',
      shadow: 2,
      bgOpacity: 0,
      bgColor: '#000000',
      position: 'bottom',
      marginV: 48,
      bold: true,
      italic: false,
      uppercase: true,
    },
  },
  {
    id: 'boxed',
    label: 'Boxed',
    patch: {
      font: 'roboto',
      fontSize: 38,
      color: '#ffffff',
      outline: 0,
      outlineColor: '#000000',
      shadow: 0,
      bgOpacity: 0.72,
      bgColor: '#000000',
      position: 'bottom',
      marginV: 36,
      bold: false,
      italic: false,
      uppercase: false,
    },
  },
  {
    id: 'meme',
    label: 'Meme',
    patch: {
      font: 'oswald',
      fontSize: 54,
      color: '#ffffff',
      outline: 4,
      outlineColor: '#000000',
      shadow: 2,
      bgOpacity: 0,
      bgColor: '#000000',
      position: 'bottom',
      marginV: 32,
      bold: true,
      italic: false,
      uppercase: true,
    },
  },
]

export function defaultSubtitleStyle(): SubtitleStyle {
  return {
    enabled: false,
    font: 'inter',
    fontSize: 40,
    color: '#ffffff',
    outline: 2.5,
    outlineColor: '#000000',
    shadow: 1.5,
    bgOpacity: 0,
    bgColor: '#000000',
    position: 'bottom',
    marginV: 36,
    bold: false,
    italic: false,
    uppercase: false,
  }
}

export function fontById(id: string): SubtitleFont {
  return SUBTITLE_FONTS.find((f) => f.id === id) ?? SUBTITLE_FONTS[0]
}

function clamp(v: number, min: number, max: number, fallback: number): number {
  if (!Number.isFinite(v)) return fallback
  return Math.min(max, Math.max(min, v))
}

function normalizeHex(raw: unknown, fallback: string): string {
  if (typeof raw !== 'string') return fallback
  const clean = raw.trim().replace(/^#/, '').toLowerCase()
  return /^[0-9a-f]{6}$/.test(clean) ? `#${clean}` : fallback
}

export function sanitizeSubtitleStyle(raw: Partial<SubtitleStyle> | null | undefined): SubtitleStyle {
  const def = defaultSubtitleStyle()
  if (!raw || typeof raw !== 'object') return def
  const pos = raw.position === 'top' || raw.position === 'middle' || raw.position === 'bottom'
    ? raw.position
    : def.position
  return {
    enabled: Boolean(raw.enabled),
    font: fontById(typeof raw.font === 'string' ? raw.font : def.font).id,
    fontSize: Math.round(clamp(Number(raw.fontSize ?? def.fontSize), 16, 96, def.fontSize)),
    color: normalizeHex(raw.color, def.color),
    outline: Math.round(clamp(Number(raw.outline ?? def.outline), 0, 6, def.outline) * 10) / 10,
    outlineColor: normalizeHex(raw.outlineColor, def.outlineColor),
    shadow: Math.round(clamp(Number(raw.shadow ?? def.shadow), 0, 6, def.shadow) * 10) / 10,
    bgOpacity: Math.round(clamp(Number(raw.bgOpacity ?? def.bgOpacity), 0, 1, def.bgOpacity) * 100) / 100,
    bgColor: normalizeHex(raw.bgColor, def.bgColor),
    position: pos,
    marginV: Math.round(clamp(Number(raw.marginV ?? def.marginV), 0, 220, def.marginV)),
    bold: Boolean(raw.bold),
    italic: Boolean(raw.italic),
    uppercase: Boolean(raw.uppercase),
  }
}

export function loadSubtitleStyle(): SubtitleStyle {
  try {
    const raw = window.localStorage?.getItem(STORAGE_KEY)
    if (!raw) return defaultSubtitleStyle()
    return sanitizeSubtitleStyle(JSON.parse(raw) as Partial<SubtitleStyle>)
  } catch {
    return defaultSubtitleStyle()
  }
}

export function saveSubtitleStyle(style: SubtitleStyle): void {
  try {
    window.localStorage?.setItem(STORAGE_KEY, JSON.stringify(sanitizeSubtitleStyle(style)))
  } catch {
    // Ignore storage quota or private-mode errors.
  }
}

/**
 * Registers @font-face rules pointing at Quipster's bundled /api/fonts/*.ttf
 * endpoint so the browser preview renders with the exact same Google Fonts
 * binaries that FFmpeg's libass loads on the server.
 */
export function ensureFontFacesLoaded(fonts: readonly SubtitleFont[] = SUBTITLE_FONTS): void {
  if (typeof document === 'undefined' || document.getElementById(FONT_STYLE_TAG_ID)) return
  const rules: string[] = []
  for (const f of fonts) {
    rules.push(
      `@font-face { font-family: "${f.family}"; src: url("/api/fonts/${f.id}-400.ttf") format("truetype"); font-weight: 400; font-style: normal; font-display: swap; }`,
    )
    if (f.hasBold) {
      rules.push(
        `@font-face { font-family: "${f.family}"; src: url("/api/fonts/${f.id}-700.ttf") format("truetype"); font-weight: 700; font-style: normal; font-display: swap; }`,
      )
    }
  }
  const el = document.createElement('style')
  el.id = FONT_STYLE_TAG_ID
  el.textContent = rules.join('\n')
  document.head.appendChild(el)
}

/**
 * Returns the subtitle lines active at mediaTimeSec. Before playback advances
 * past the start (previewAtStart), falls back to the first selected cue so
 * users tweaking style controls immediately see live feedback.
 */
export function activeSubtitleLines(
  cues: readonly CueDTO[],
  mediaTimeSec: number,
  uppercase: boolean,
  previewAtStart = false,
): string[] {
  if (!cues.length) return []
  const matched = cues
    .filter((c) => mediaTimeSec >= c.start && mediaTimeSec <= c.end)
    .map((c) => c.text.trim())
    .filter(Boolean)
  const lines = matched.length > 0 ? matched : previewAtStart ? [cues[0].text.trim()].filter(Boolean) : []
  return uppercase ? lines.map((line) => line.toUpperCase()) : lines
}

/**
 * Computes the rendered video rectangle inside an object-fit: contain box so
 * the subtitle overlay stays pinned inside the actual video frame instead of
 * drifting into letterboxing or pillarboxing bars.
 */
export function computeVideoContentBox(
  containerW: number,
  containerH: number,
  videoW: number,
  videoH: number,
): VideoContentBox {
  if (containerW <= 0 || containerH <= 0) {
    return { left: 0, top: 0, width: 0, height: 0 }
  }
  if (videoW <= 0 || videoH <= 0) {
    return { left: 0, top: 0, width: containerW, height: containerH }
  }
  const containerRatio = containerW / containerH
  const videoRatio = videoW / videoH
  if (videoRatio > containerRatio) {
    const height = containerW / videoRatio
    return { left: 0, top: (containerH - height) / 2, width: containerW, height }
  }
  const width = containerH * videoRatio
  return { left: (containerW - width) / 2, top: 0, width, height: containerH }
}

function hexToRgba(hex: string, alpha: number): string {
  const clean = normalizeHex(hex, '#000000').slice(1)
  const r = parseInt(clean.slice(0, 2), 16)
  const g = parseInt(clean.slice(2, 4), 16)
  const b = parseInt(clean.slice(4, 6), 16)
  return `rgba(${r}, ${g}, ${b}, ${alpha})`
}

/**
 * Converts a SubtitleStyle and rendered video height into inline CSS for the
 * overlay container and caption text, matching libass's PlayResY=720 geometry.
 */
export function subtitleOverlayStyles(
  style: SubtitleStyle,
  videoHeightPx: number,
): { container: CSSProperties; text: CSSProperties } {
  const scale = Math.max(videoHeightPx, 1) / SUBTITLE_PLAY_RES_Y
  const font = fontById(style.font)
  const marginVPx = style.marginV * scale
  const marginHPx = ASS_MARGIN_H * scale
  const justifyContent =
    style.position === 'top' ? 'flex-start' : style.position === 'middle' ? 'center' : 'flex-end'

  const container: CSSProperties = {
    justifyContent,
    paddingLeft: `${marginHPx.toFixed(1)}px`,
    paddingRight: `${marginHPx.toFixed(1)}px`,
    paddingTop: style.position === 'top' ? `${marginVPx.toFixed(1)}px` : '0px',
    paddingBottom: style.position === 'bottom' ? `${marginVPx.toFixed(1)}px` : '0px',
  }

  const text: CSSProperties = {
    fontFamily: `"${font.family}", ${font.category}`,
    fontSize: `${(style.fontSize * scale).toFixed(2)}px`,
    color: style.color,
    fontWeight: style.bold ? 700 : 400,
    fontStyle: style.italic ? 'italic' : 'normal',
    lineHeight: 1.22,
  }

  if (style.bgOpacity > 0) {
    const padPx = Math.max(2, ASS_BOX_PADDING * scale)
    text.backgroundColor = hexToRgba(style.bgColor, style.bgOpacity)
    text.padding = `${(padPx * 0.45).toFixed(1)}px ${padPx.toFixed(1)}px`
    text.borderRadius = `${Math.max(2, 4 * scale).toFixed(1)}px`
  } else {
    const outlinePx = style.outline * scale
    if (outlinePx > 0) {
      text.paintOrder = 'stroke fill'
      text.WebkitTextStroke = `${(outlinePx * 2).toFixed(2)}px ${style.outlineColor}`
    }
    const shadowPx = style.shadow * scale
    if (shadowPx > 0) {
      const off = Math.max(1, shadowPx).toFixed(2)
      const blur = Math.max(1, shadowPx * 0.8).toFixed(2)
      text.textShadow = `${off}px ${off}px ${blur}px rgba(0, 0, 0, 0.72)`
    }
  }

  return { container, text }
}

/**
 * Serializes an enabled SubtitleStyle into compact /api/clips/stream query
 * parameters so Download and Copy link reproduce the exact styled burn-in.
 */
export function subtitleQueryParams(style: SubtitleStyle | null | undefined): Record<string, string> {
  if (!style || !style.enabled) return {}
  const s = sanitizeSubtitleStyle(style)
  const params: Record<string, string> = {
    subs: '1',
    subFont: s.font,
    subSize: String(s.fontSize),
    subColor: s.color.slice(1),
    subOutline: String(s.outline),
    subOutlineColor: s.outlineColor.slice(1),
    subShadow: String(s.shadow),
    subBg: String(s.bgOpacity),
    subBgColor: s.bgColor.slice(1),
    subPos: s.position,
    subMargin: String(s.marginV),
  }
  if (s.bold) params.subBold = '1'
  if (s.italic) params.subItalic = '1'
  if (s.uppercase) params.subUpper = '1'
  return params
}
