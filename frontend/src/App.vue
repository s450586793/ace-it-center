<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { APIError, apiRequest } from './api'
import AuthScreen from './components/AuthScreen.vue'
import OperationsWorkspace from './components/OperationsWorkspace.vue'
import type { Node, NodeGroup, Owner, PairingRequest } from './types'

type Phase = 'loading' | 'setup' | 'login' | 'ready' | 'fatal'

const phase = ref<Phase>('loading')
const owner = ref<Owner | null>(null)
const groups = ref<NodeGroup[]>([])
const nodes = ref<Node[]>([])
const pairings = ref<PairingRequest[]>([])
const busy = ref(false)
const error = ref('')
const pairingError = ref('')
let nodePoll: ReturnType<typeof setInterval> | undefined
let nodeRefresh: Promise<void> | null = null
let pairingRefresh: Promise<void> | null = null

function stopNodePolling() {
  if (nodePoll !== undefined) {
    clearInterval(nodePoll)
    nodePoll = undefined
  }
  document.removeEventListener('visibilitychange', refreshVisibleNodes)
}

function refreshVisibleNodes() {
  if (phase.value === 'ready' && document.visibilityState === 'visible') {
    void Promise.all([loadNodes(), loadPairings()])
  }
}

function startNodePolling() {
  if (phase.value !== 'ready') return
  stopNodePolling()
  document.addEventListener('visibilitychange', refreshVisibleNodes)
  nodePoll = setInterval(refreshVisibleNodes, 30_000)
}

function loadNodes(): Promise<void> {
  if (nodeRefresh) return nodeRefresh

  nodeRefresh = apiRequest<{ items: Node[] }>('/api/v1/nodes')
    .then(response => {
      nodes.value = response.items
      error.value = ''
    })
    .catch(requestError => {
      if (requestError instanceof APIError && requestError.status === 401) {
        owner.value = null
        phase.value = 'login'
        stopNodePolling()
        return
      }
      error.value = requestError instanceof Error ? requestError.message : 'Unable to refresh devices'
    })
    .finally(() => {
      nodeRefresh = null
    })

  return nodeRefresh
}

function loadPairings(): Promise<void> {
  if (pairingRefresh) return pairingRefresh

  pairingRefresh = apiRequest<{ items: PairingRequest[] }>('/api/v1/pairings')
    .then(response => {
      pairings.value = response.items
      pairingError.value = ''
    })
    .catch(requestError => {
      if (requestError instanceof APIError && requestError.status === 401) {
        owner.value = null
        phase.value = 'login'
        stopNodePolling()
        return
      }
      pairingError.value = '无法刷新待配对设备，请稍后重试'
    })
    .finally(() => {
      pairingRefresh = null
    })

  return pairingRefresh
}

async function initialize() {
  error.value = ''
  pairingError.value = ''
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
      startNodePolling()
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
  pairingError.value = ''
  try {
    const endpoint = phase.value === 'setup' ? '/api/v1/auth/setup' : '/api/v1/auth/login'
    const response = await apiRequest<{ owner: Owner }>(endpoint, {
      method: 'POST', body: JSON.stringify(credentials),
    })
    owner.value = response.owner
    phase.value = 'ready'
    await loadData()
    startNodePolling()
  } catch (requestError) {
    error.value = requestError instanceof Error ? requestError.message : 'Authentication failed'
  } finally {
    busy.value = false
  }
}

async function loadData() {
  const [groupData] = await Promise.all([
    apiRequest<{ items: NodeGroup[] }>('/api/v1/groups'),
    loadNodes(),
    loadPairings(),
  ])
  groups.value = groupData.items
}

async function logout() {
  await apiRequest('/api/v1/auth/logout', { method: 'POST' })
  owner.value = null
  phase.value = 'login'
  stopNodePolling()
}

onMounted(initialize)
onBeforeUnmount(stopNodePolling)
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
  <template v-else-if="phase === 'ready' && owner">
    <OperationsWorkspace
      :owner="owner"
      :groups="groups"
      :nodes="nodes"
      :pairings="pairings"
      @refresh="loadData"
      @logout="logout"
    />
    <p v-if="pairingError || error" role="alert">{{ pairingError || error }}</p>
  </template>
  <main v-else class="fatal-screen">
    <div class="brand-mark" aria-hidden="true"><span>!</span></div>
    <h1>控制平面不可用</h1>
    <p>{{ error }}</p>
    <button class="primary-button" type="button" @click="initialize">重试连接</button>
  </main>
</template>
