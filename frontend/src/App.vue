<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { APIError, apiRequest } from './api'
import AuthScreen from './components/AuthScreen.vue'
import OperationsWorkspace from './components/OperationsWorkspace.vue'
import type { Node, NodeGroup, Organization, Owner, Site } from './types'

type Phase = 'loading' | 'setup' | 'login' | 'ready' | 'fatal'

const phase = ref<Phase>('loading')
const owner = ref<Owner | null>(null)
const organizations = ref<Organization[]>([])
const sites = ref<Site[]>([])
const groups = ref<NodeGroup[]>([])
const nodes = ref<Node[]>([])
const busy = ref(false)
const error = ref('')

async function initialize() {
  error.value = ''
  try {
    const status = await apiRequest<{ setup: boolean }>('/api/v1/auth/status')
    if (!status.setup) {
      phase.value = 'setup'
      return
    }
    try {
      const me = await apiRequest<{ owner: Owner }>('/api/v1/auth/me')
      owner.value = me.owner
      phase.value = 'ready'
      await loadData()
    } catch (requestError) {
      if (requestError instanceof APIError && requestError.status === 401) {
        phase.value = 'login'
        return
      }
      throw requestError
    }
  } catch (requestError) {
    error.value = requestError instanceof Error ? requestError.message : 'Service unavailable'
    phase.value = 'fatal'
  }
}

async function submitCredentials(credentials: { username: string; password: string }) {
  busy.value = true
  error.value = ''
  try {
    const endpoint = phase.value === 'setup' ? '/api/v1/auth/setup' : '/api/v1/auth/login'
    const response = await apiRequest<{ owner: Owner }>(endpoint, {
      method: 'POST', body: JSON.stringify(credentials),
    })
    owner.value = response.owner
    phase.value = 'ready'
    await loadData()
  } catch (requestError) {
    error.value = requestError instanceof Error ? requestError.message : 'Authentication failed'
  } finally {
    busy.value = false
  }
}

async function loadData() {
  const [organizationData, siteData, groupData, nodeData] = await Promise.all([
    apiRequest<{ items: Organization[] }>('/api/v1/organizations'),
    apiRequest<{ items: Site[] }>('/api/v1/sites'),
    apiRequest<{ items: NodeGroup[] }>('/api/v1/groups'),
    apiRequest<{ items: Node[] }>('/api/v1/nodes'),
  ])
  organizations.value = organizationData.items
  sites.value = siteData.items
  groups.value = groupData.items
  nodes.value = nodeData.items
}

async function logout() {
  await apiRequest('/api/v1/auth/logout', { method: 'POST' })
  owner.value = null
  phase.value = 'login'
}

onMounted(initialize)
</script>

<template>
  <div v-if="phase === 'loading'" class="boot-screen" aria-live="polite">
    <div class="brand-mark" aria-hidden="true"><span>A</span></div>
    <span>ACE CONTROL PLANE</span>
  </div>
  <AuthScreen
    v-else-if="phase === 'setup' || phase === 'login'"
    :mode="phase"
    :busy="busy"
    :error="error"
    @submit="submitCredentials"
  />
  <OperationsWorkspace
    v-else-if="phase === 'ready' && owner"
    :owner="owner"
    :organizations="organizations"
    :sites="sites"
    :groups="groups"
    :nodes="nodes"
    @refresh="loadData"
    @logout="logout"
  />
  <main v-else class="fatal-screen">
    <div class="brand-mark" aria-hidden="true"><span>!</span></div>
    <h1>控制平面不可用</h1>
    <p>{{ error }}</p>
    <button class="primary-button" type="button" @click="initialize">重试连接</button>
  </main>
</template>

