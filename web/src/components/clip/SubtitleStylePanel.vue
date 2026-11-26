<template>
  <section class="sub-editor" :class="{ open: expanded }">
    <header class="sub-editor-head">
      <button
        type="button"
        class="sub-collapse-btn"
        :aria-expanded="expanded"
        aria-controls="subtitle-style-body"
        @click="expanded = !expanded"
      >
        <svg class="chevron" :class="{ rotated: expanded }" width="14" height="14" viewBox="0 0 16 16" aria-hidden="true">
          <path d="M4.5 6l3.5 4 3.5-4" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
        </svg>
        <span class="panel-title">Subtitles</span>
        <span class="sub-status-pill" :class="{ on: modelValue.enabled }">
          {{ modelValue.enabled ? 'Burn-in on' : 'Off' }}
        </span>
      </button>

      <label class="sub-switch" :title="modelValue.enabled ? 'Disable burned-in subtitles' : 'Embed subtitles on video'">
        <input
          type="checkbox"
          class="sub-switch-input"
          aria-label="Embed subtitles"
          :checked="modelValue.enabled"
          @change="onToggleEnabled(($event.target as HTMLInputElement).checked)"
        />
        <span class="sub-switch-track" aria-hidden="true">
          <span class="sub-switch-thumb" />
        </span>
      </label>
    </header>

    <div v-if="expanded" id="subtitle-style-body" class="sub-editor-body">
      <div class="sub-presets" role="group" aria-label="Subtitle presets">
        <button
          v-for="preset in SUBTITLE_PRESETS"
          :key="preset.id"
          type="button"
          class="preset-chip"
          @click="applyPreset(preset)"
        >
          {{ preset.label }}
        </button>
      </div>

      <div class="sub-row">
        <label class="sub-field sub-field-grow">
          <span class="sub-label">Font</span>
          <select
            class="sub-select"
            aria-label="Subtitle font"
            :value="modelValue.font"
            @change="patch({ font: ($event.target as HTMLSelectElement).value })"
          >
            <option
              v-for="f in SUBTITLE_FONTS"
              :key="f.id"
              :value="f.id"
              :style="{ fontFamily: `&quot;${f.family}&quot;, ${f.category}` }"
            >
              {{ f.family }}
            </option>
          </select>
        </label>

        <div class="sub-field">
          <span class="sub-label">Style</span>
          <div class="fmt-group" role="group" aria-label="Text formatting">
            <button
              type="button"
              class="fmt-btn"
              :class="{ active: modelValue.bold }"
              :aria-pressed="modelValue.bold"
              title="Bold"
              @click="patch({ bold: !modelValue.bold })"
            >
              <strong>B</strong>
            </button>
            <button
              type="button"
              class="fmt-btn"
              :class="{ active: modelValue.italic }"
              :aria-pressed="modelValue.italic"
              title="Italic"
              @click="patch({ italic: !modelValue.italic })"
            >
              <em>I</em>
            </button>
            <button
              type="button"
              class="fmt-btn"
              :class="{ active: modelValue.uppercase }"
              :aria-pressed="modelValue.uppercase"
              title="All caps"
              @click="patch({ uppercase: !modelValue.uppercase })"
            >
              AA
            </button>
          </div>
        </div>
      </div>

      <label class="sub-slider-row">
        <span class="sub-slider-head">
          <span class="sub-label">Font size</span>
          <span class="mono sub-val">{{ modelValue.fontSize }}px</span>
        </span>
        <input
          type="range"
          min="16"
          max="96"
          step="1"
          aria-label="Subtitle font size"
          :value="modelValue.fontSize"
          @input="patch({ fontSize: Number(($event.target as HTMLInputElement).value) })"
        />
      </label>

      <div class="sub-field">
        <span class="sub-label">Text color</span>
        <div class="swatch-row">
          <button
            v-for="sw in TEXT_COLOR_SWATCHES"
            :key="sw.hex"
            type="button"
            class="swatch"
            :class="{ active: modelValue.color.toLowerCase() === sw.hex }"
            :style="{ background: sw.hex }"
            :title="sw.label"
            :aria-label="`Text color ${sw.label}`"
            @click="patch({ color: sw.hex })"
          />
          <label class="color-picker-wrap" title="Custom text color">
            <input
              type="color"
              class="color-input"
              aria-label="Custom text color"
              :value="modelValue.color"
              @input="patch({ color: ($event.target as HTMLInputElement).value })"
            />
          </label>
        </div>
      </div>

      <div class="sub-row">
        <div class="sub-field sub-field-grow">
          <span class="sub-label">Position</span>
          <div class="pos-group" role="group" aria-label="Subtitle vertical position">
            <button
              v-for="pos in positions"
              :key="pos.value"
              type="button"
              class="pos-btn"
              :class="{ active: modelValue.position === pos.value }"
              :aria-pressed="modelValue.position === pos.value"
              @click="patch({ position: pos.value })"
            >
              {{ pos.label }}
            </button>
          </div>
        </div>
      </div>

      <label class="sub-slider-row">
        <span class="sub-slider-head">
          <span class="sub-label">Vertical margin</span>
          <span class="mono sub-val">{{ modelValue.marginV }}px</span>
        </span>
        <input
          type="range"
          min="0"
          max="220"
          step="4"
          aria-label="Subtitle vertical margin"
          :value="modelValue.marginV"
          @input="patch({ marginV: Number(($event.target as HTMLInputElement).value) })"
        />
      </label>

      <label class="sub-slider-row">
        <span class="sub-slider-head">
          <span class="sub-label">Background box</span>
          <span class="mono sub-val">{{ Math.round(modelValue.bgOpacity * 100) }}%</span>
        </span>
        <input
          type="range"
          min="0"
          max="1"
          step="0.05"
          aria-label="Subtitle background opacity"
          :value="modelValue.bgOpacity"
          @input="patch({ bgOpacity: Number(($event.target as HTMLInputElement).value) })"
        />
      </label>

      <div v-if="modelValue.bgOpacity > 0" class="sub-field">
        <span class="sub-label">Box color</span>
        <div class="swatch-row">
          <button
            v-for="sw in OUTLINE_COLOR_SWATCHES"
            :key="sw.hex"
            type="button"
            class="swatch"
            :class="{ active: modelValue.bgColor.toLowerCase() === sw.hex }"
            :style="{ background: sw.hex }"
            :title="sw.label"
            :aria-label="`Box color ${sw.label}`"
            @click="patch({ bgColor: sw.hex })"
          />
          <label class="color-picker-wrap" title="Custom box color">
            <input
              type="color"
              class="color-input"
              aria-label="Custom box color"
              :value="modelValue.bgColor"
              @input="patch({ bgColor: ($event.target as HTMLInputElement).value })"
            />
          </label>
        </div>
      </div>

      <template v-else>
        <label class="sub-slider-row">
          <span class="sub-slider-head">
            <span class="sub-label">Outline</span>
            <span class="mono sub-val">{{ modelValue.outline.toFixed(1) }}</span>
          </span>
          <input
            type="range"
            min="0"
            max="6"
            step="0.5"
            aria-label="Subtitle outline thickness"
            :value="modelValue.outline"
            @input="patch({ outline: Number(($event.target as HTMLInputElement).value) })"
          />
        </label>

        <div v-if="modelValue.outline > 0" class="sub-field">
          <span class="sub-label">Outline color</span>
          <div class="swatch-row">
            <button
              v-for="sw in OUTLINE_COLOR_SWATCHES"
              :key="sw.hex"
              type="button"
              class="swatch"
              :class="{ active: modelValue.outlineColor.toLowerCase() === sw.hex }"
              :style="{ background: sw.hex }"
              :title="sw.label"
              :aria-label="`Outline color ${sw.label}`"
              @click="patch({ outlineColor: sw.hex })"
            />
            <label class="color-picker-wrap" title="Custom outline color">
              <input
                type="color"
                class="color-input"
                aria-label="Custom outline color"
                :value="modelValue.outlineColor"
                @input="patch({ outlineColor: ($event.target as HTMLInputElement).value })"
              />
            </label>
          </div>
        </div>

        <label class="sub-slider-row">
          <span class="sub-slider-head">
            <span class="sub-label">Drop shadow</span>
            <span class="mono sub-val">{{ modelValue.shadow.toFixed(1) }}</span>
          </span>
          <input
            type="range"
            min="0"
            max="6"
            step="0.5"
            aria-label="Subtitle drop shadow"
            :value="modelValue.shadow"
            @input="patch({ shadow: Number(($event.target as HTMLInputElement).value) })"
          />
        </label>
      </template>

      <footer class="sub-editor-foot">
        <span class="sub-note">Live preview matches Precise &amp; GIF export.</span>
        <button type="button" class="link-btn" @click="resetStyle">Reset</button>
      </footer>
    </div>
  </section>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import {
  defaultSubtitleStyle,
  OUTLINE_COLOR_SWATCHES,
  SUBTITLE_FONTS,
  SUBTITLE_PRESETS,
  TEXT_COLOR_SWATCHES,
  type SubtitlePosition,
  type SubtitlePreset,
  type SubtitleStyle,
} from '../../lib/subtitles'

const props = defineProps<{
  modelValue: SubtitleStyle
}>()

const emit = defineEmits<{
  'update:modelValue': [value: SubtitleStyle]
}>()

const local = ref<SubtitleStyle>({ ...props.modelValue })
watch(
  () => props.modelValue,
  (next) => {
    local.value = { ...next }
  },
  { deep: true },
)

const expanded = ref(props.modelValue.enabled)

const positions: { value: SubtitlePosition; label: string }[] = [
  { value: 'top', label: 'Top' },
  { value: 'middle', label: 'Middle' },
  { value: 'bottom', label: 'Bottom' },
]

function commit(next: SubtitleStyle) {
  local.value = next
  emit('update:modelValue', next)
}

function patch(updates: Partial<SubtitleStyle>) {
  commit({ ...local.value, ...updates })
}

function onToggleEnabled(enabled: boolean) {
  if (enabled) expanded.value = true
  patch({ enabled })
}

function applyPreset(preset: SubtitlePreset) {
  commit({ ...local.value, ...preset.patch, enabled: true })
}

function resetStyle() {
  commit({ ...defaultSubtitleStyle(), enabled: local.value.enabled })
}
</script>

<style scoped>
.sub-editor {
  border: 1px solid var(--border);
  border-radius: 12px;
  background: rgba(255, 255, 255, 0.015);
  overflow: hidden;
  flex-shrink: 0;
}
.sub-editor-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 9px 12px;
}
.sub-editor.open .sub-editor-head {
  border-bottom: 1px solid var(--border);
}
.sub-collapse-btn {
  flex: 1;
  min-width: 0;
  display: flex;
  align-items: center;
  gap: 7px;
  background: transparent;
  border: none;
  padding: 2px 0;
  text-align: left;
  color: var(--text);
  cursor: pointer;
}
.chevron {
  color: var(--muted);
  transition: transform 140ms ease;
  flex-shrink: 0;
}
.chevron.rotated {
  transform: rotate(180deg);
}
.panel-title {
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.1em;
  text-transform: uppercase;
  color: var(--faint);
}
.sub-status-pill {
  font-size: 10.5px;
  font-weight: 500;
  padding: 1px 7px;
  border-radius: 999px;
  border: 1px solid var(--border);
  color: var(--muted);
}
.sub-status-pill.on {
  border-color: var(--accent-line);
  background: var(--accent-soft);
  color: var(--accent);
}
.sub-switch {
  position: relative;
  display: inline-flex;
  align-items: center;
  cursor: pointer;
  flex-shrink: 0;
}
.sub-switch-input {
  position: absolute;
  opacity: 0;
  width: 0;
  height: 0;
}
.sub-switch-track {
  width: 32px;
  height: 18px;
  border-radius: 999px;
  background: rgba(255, 255, 255, 0.12);
  border: 1px solid var(--border-strong);
  position: relative;
  transition: background 140ms ease, border-color 140ms ease;
}
.sub-switch-thumb {
  position: absolute;
  top: 2px;
  left: 2px;
  width: 12px;
  height: 12px;
  border-radius: 50%;
  background: var(--muted);
  transition: transform 140ms ease, background 140ms ease;
}
.sub-switch-input:checked + .sub-switch-track {
  background: var(--accent-soft);
  border-color: var(--accent-line);
}
.sub-switch-input:checked + .sub-switch-track .sub-switch-thumb {
  transform: translateX(14px);
  background: var(--accent);
}
.sub-switch-input:focus-visible + .sub-switch-track {
  outline: 2px solid var(--accent);
  outline-offset: 2px;
}

.sub-editor-body {
  padding: 12px;
  display: flex;
  flex-direction: column;
  gap: 10px;
  max-height: 290px;
  overflow-y: auto;
}
.sub-presets {
  display: flex;
  flex-wrap: wrap;
  gap: 5px;
}
.preset-chip {
  font-size: 11px;
  font-weight: 500;
  padding: 3px 8px;
  border-radius: 6px;
  border: 1px solid var(--border);
  background: rgba(255, 255, 255, 0.03);
  color: var(--muted);
  cursor: pointer;
}
.preset-chip:hover {
  color: var(--text);
  border-color: var(--border-strong);
  background: rgba(255, 255, 255, 0.07);
}
.sub-row {
  display: flex;
  gap: 8px;
  align-items: flex-end;
}
.sub-field {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.sub-field-grow {
  flex: 1;
  min-width: 0;
}
.sub-label {
  font-size: 10.5px;
  font-weight: 600;
  letter-spacing: 0.06em;
  text-transform: uppercase;
  color: var(--faint);
}
.sub-select {
  width: 100%;
  height: 30px;
  padding: 0 8px;
  border-radius: 7px;
  border: 1px solid var(--border-strong);
  background: var(--surface-2);
  color: var(--text);
  font-size: 12.5px;
}
.fmt-group,
.pos-group {
  display: inline-flex;
  border: 1px solid var(--border-strong);
  border-radius: 7px;
  overflow: hidden;
}
.pos-group {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  width: 100%;
}
.fmt-btn,
.pos-btn {
  height: 30px;
  padding: 0 9px;
  border: none;
  border-right: 1px solid var(--border);
  background: transparent;
  color: var(--muted);
  font-size: 11.5px;
  cursor: pointer;
}
.fmt-btn:last-child,
.pos-btn:last-child {
  border-right: none;
}
.fmt-btn:hover,
.pos-btn:hover {
  color: var(--text);
  background: rgba(255, 255, 255, 0.05);
}
.fmt-btn.active,
.pos-btn.active {
  background: var(--accent-soft);
  color: var(--accent);
}
.sub-slider-row {
  display: flex;
  flex-direction: column;
  gap: 3px;
}
.sub-slider-head {
  display: flex;
  justify-content: space-between;
  align-items: baseline;
}
.sub-val {
  font-size: 11px;
  color: var(--muted);
}
.sub-slider-row input[type='range'] {
  width: 100%;
  accent-color: var(--accent);
  height: 4px;
  cursor: pointer;
}
.swatch-row {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.swatch {
  width: 20px;
  height: 20px;
  border-radius: 50%;
  border: 1px solid rgba(255, 255, 255, 0.25);
  cursor: pointer;
  padding: 0;
}
.swatch.active {
  outline: 2px solid var(--accent);
  outline-offset: 2px;
}
.color-picker-wrap {
  display: inline-flex;
  align-items: center;
}
.color-input {
  width: 24px;
  height: 22px;
  padding: 0;
  border: 1px solid var(--border-strong);
  border-radius: 5px;
  background: transparent;
  cursor: pointer;
}
.sub-editor-foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding-top: 4px;
  border-top: 1px solid var(--border);
}
.sub-note {
  font-size: 10.5px;
  color: var(--faint);
}
.link-btn {
  background: none;
  border: none;
  padding: 0;
  font-size: 11px;
  color: var(--muted);
  text-decoration: underline;
  text-underline-offset: 3px;
  cursor: pointer;
}
.link-btn:hover {
  color: var(--text);
}
</style>
