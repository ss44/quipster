import { createVuetify } from 'vuetify'
import { md3 } from 'vuetify/blueprints'

export default createVuetify({
  blueprint: md3,
  theme: {
    defaultTheme: 'quipsterDark',
    themes: {
      quipsterDark: {
        dark: true,
        colors: {
          primary: '#ffd166',
          'on-primary': '#141414',
          secondary: '#9aa1ac',
          'on-secondary': '#101216',
          background: '#0a0b0d',
          surface: '#101216',
          'surface-bright': '#1b1f26',
          'surface-variant': '#15181e',
          'on-surface': '#eceef1',
          'on-surface-variant': '#9aa1ac',
          error: '#f87171',
          success: '#4ade80',
          warning: '#fbbf24',
          info: '#93c5fd',
          outline: 'rgba(255,255,255,0.12)',
        },
      },
    },
  },
  defaults: {
    VBtn: { rounded: 'lg', class: 'text-none', elevation: 0 },
    VCard: { rounded: 'lg', elevation: 0, color: 'surface' },
    VTextField: { variant: 'outlined', density: 'comfortable', hideDetails: 'auto' },
    VSelect: { variant: 'outlined', density: 'comfortable', hideDetails: 'auto' },
    VCombobox: { variant: 'outlined', density: 'comfortable', hideDetails: 'auto' },
    VChip: { rounded: 'lg' },
    VDialog: { scrim: 'rgba(5,6,8,0.72)' },
    VTooltip: { location: 'bottom' },
  },
})