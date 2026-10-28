<template>
  <div class="admin-shell">
    <nav class="admin-tabs" aria-label="Settings sections">
      <RouterLink to="/admin/library" class="admin-tab" active-class="is-active">
        <v-icon icon="mdi-tune-variant" size="16" class="mr-1" />
        Library
      </RouterLink>
      <RouterLink to="/admin/diagnostics" class="admin-tab" active-class="is-active">
        <v-icon icon="mdi-pulse" size="16" class="mr-1" />
        Diagnostics
      </RouterLink>
      <RouterLink to="/admin/nodes" class="admin-tab" active-class="is-active">
        <v-icon icon="mdi-server-network" size="16" class="mr-1" />
        Nodes
      </RouterLink>
    </nav>
    <RouterView />
  </div>
</template>

<style scoped>
.admin-shell {
  min-height: 100%;
}
.admin-tabs {
  display: flex;
  gap: 4px;
  max-width: 1520px;
  margin: 0 auto;
  padding: 14px 24px 0;
  border-bottom: 1px solid var(--border);
}
.admin-tab {
  display: inline-flex;
  align-items: center;
  padding: 8px 12px 10px;
  margin-bottom: -1px;
  border-bottom: 2px solid transparent;
  color: var(--muted);
  font-size: 13px;
  font-weight: 550;
  text-decoration: none;
}
.admin-tab:hover {
  color: var(--text);
}
.admin-tab.is-active {
  color: var(--text);
  border-bottom-color: var(--accent);
}
@media (max-width: 640px) {
  .admin-tabs {
    padding: 10px 14px 0;
  }
}
</style>
