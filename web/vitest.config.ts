import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'

// Standalone Vitest configuration. The Vuetify build plugin (auto-import +
// per-component CSS) is intentionally not used here: tests register the
// Vuetify components they need explicitly, which avoids pulling stylesheet
// imports that Node cannot resolve.
export default defineConfig({
  plugins: [vue()],
  test: {
    environment: 'happy-dom',
    setupFiles: ['./vitest.setup.ts'],
    // Vuetify imports its component stylesheets, which Node cannot resolve on
    // its own; inlining routes those imports through Vite's transform pipeline.
    server: {
      deps: {
        inline: ['vuetify'],
      },
    },
  },
})
