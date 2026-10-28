import { createRouter, createWebHistory } from 'vue-router'

export default createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', name: 'search', component: () => import('./components/SearchView.vue') },
    {
      path: '/admin',
      component: () => import('./components/AdminView.vue'),
      redirect: '/admin/library',
      children: [
        { path: 'library', name: 'admin-library', component: () => import('./components/LibraryPanel.vue') },
        { path: 'diagnostics', name: 'admin-diagnostics', component: () => import('./components/DiagnosticsView.vue') },
        { path: 'nodes', name: 'admin-nodes', component: () => import('./components/NodesView.vue') },
      ],
    },
  ],
})
