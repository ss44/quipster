import { defineConfig, loadEnv } from 'vite'
import vue from '@vitejs/plugin-vue'
import vuetify from 'vite-plugin-vuetify'

export default defineConfig(({ mode }) => {
  // Social crawlers do not resolve relative image paths, so og:image and
  // twitter:image are rewritten to absolute URLs when a site URL is provided.
  const siteURL = (loadEnv(mode, process.cwd(), '').VITE_SITE_URL || '').replace(/\/+$/, '')

  return {
    plugins: [
      vue(),
      vuetify({ autoImport: true }),
      {
        name: 'absolute-social-image',
        transformIndexHtml(html) {
          if (!siteURL) return html
          return html.replaceAll('content="/og-image.png"', `content="${siteURL}/og-image.png"`)
        },
      },
    ],
    server: {
      proxy: {
        '/api': 'http://localhost:8080',
      },
    },
    build: {
      outDir: 'dist',
      emptyOutDir: true,
      chunkSizeWarningLimit: 1500,
    },
  }
})
