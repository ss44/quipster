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
          primary: '#7c4dff',
          secondary: '#00e5ff',
          background: '#101014',
          surface: '#18181f',
        },
      },
    },
  },
  defaults: {
    VBtn: { rounded: 'lg' },
    VCard: { rounded: 'lg' },
    VTextField: { variant: 'outlined', density: 'comfortable' },
    VSelect: { variant: 'outlined', density: 'comfortable' },
  },
})