import { createRouter, createWebHistory } from 'vue-router'

export default createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', name: 'search', component: () => import('./components/SearchView.vue') },
    { path: '/admin', name: 'admin', component: () => import('./components/AdminView.vue') },
  ],
})