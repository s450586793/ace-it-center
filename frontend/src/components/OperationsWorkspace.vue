<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import {
  Bell, Connection, DataBoard, Download, Expand, Fold, Monitor, Moon, Plus, Refresh,
  Sunny, SwitchButton, UploadFilled,
} from '@element-plus/icons-vue'
import { ElDialog } from 'element-plus'
import packageMetadata from '../../package.json'
import { apiRequest } from '../api'
import { isNodeOnline } from '../lib/status'
import type { Node, NodeGroup, Owner, PairingRequest } from '../types'
import AgentDownloads from './AgentDownloads.vue'
import CommandCenter from './CommandCenter.vue'
import NetworkMonitoring from './NetworkMonitoring.vue'
import NodeTable from './NodeTable.vue'
import PairingRequests from './PairingRequests.vue'
import SystemUpdate from './SystemUpdate.vue'

type WorkspaceView = 'overview' | 'network' | 'commands' | 'pairings' | 'downloads' | 'updates'

const releaseManifestPath = '/downloads/windows/stable/latest.json'
const webVersion = packageMetadata.version

const props = defineProps<{
  owner: Owner
  groups: NodeGroup[]
  nodes: Node[]
  pairings: PairingRequest[]
}>()

const emit = defineEmits<{ refresh: []; logout: []; 'session-expired': [] }>()
const now = ref(new Date())
const latestAgentVersion = ref<string | null>(null)
const activeView = ref<WorkspaceView>('overview')
const mobileNav = ref(false)
const darkMode = ref(false)
const dialog = ref<'group' | null>(null)
const actionBusy = ref(false)
const actionError = ref('')
const form = reactive({ name: '' })
let clock: number | undefined

function installerPath(version: string): string {
  return `/downloads/windows/stable/AceAgentSetup-windows-amd64-V${version}.exe`
}

function isReleaseVersion(value: unknown): value is string {
  return typeof value === 'string' && /^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(value)
}

async function loadLatestAgentVersion() {
  try {
    const response = await fetch(releaseManifestPath, { credentials: 'same-origin' })
    if (!response.ok) return

    const manifest = await response.json() as { version?: unknown; url?: unknown }
    if (!isReleaseVersion(manifest.version) || manifest.url !== installerPath(manifest.version)) return

    latestAgentVersion.value = manifest.version
  } catch {
    // 发布清单不可用时保留当前稳定版本。
  }
}

const dialogOpen = computed({
  get: () => dialog.value !== null,
  set: (open: boolean) => {
    if (!open) closeDialog()
  },
})

const onlineCount = computed(() => props.nodes.filter(node => isNodeOnline(node.last_seen_at, now.value)).length)
const offlineCount = computed(() => props.nodes.length - onlineCount.value)
const pendingPairingCount = computed(() => props.pairings.filter(pairing => pairing.state === 'pending').length)
const displayedAgentVersion = computed(() => latestAgentVersion.value ? `V${latestAgentVersion.value}` : '-')
const pageTitle = computed(() => ({
  overview: { index: 'INFRASTRUCTURE / OVERVIEW', title: '设备运行总览' },
  network: { index: 'INFRASTRUCTURE / NETWORK', title: '网络监控' },
  commands: { index: 'INFRASTRUCTURE / COMMANDS', title: '命令中心' },
  pairings: { index: 'INFRASTRUCTURE / PAIRINGS', title: '待配对设备' },
  downloads: { index: 'INFRASTRUCTURE / AGENTS', title: '客户端下载' },
  updates: { index: 'INFRASTRUCTURE / UPDATES', title: '系统升级' },
}[activeView.value]))

function showView(view: WorkspaceView) {
  activeView.value = view
  mobileNav.value = false
}

function refreshWorkspace() {
  emit('refresh')
}

function openDialog() {
  dialog.value = 'group'
  actionError.value = ''
  form.name = ''
}

function closeDialog() {
  dialog.value = null
}

async function submitDialog() {
  actionBusy.value = true
  actionError.value = ''
  try {
    await apiRequest<NodeGroup>('/api/v1/groups', { method: 'POST', body: JSON.stringify({ name: form.name }) })
    dialog.value = null
    emit('refresh')
  } catch (requestError) {
    actionError.value = requestError instanceof Error ? requestError.message : 'Operation failed'
  } finally {
    actionBusy.value = false
  }
}

function toggleTheme() {
  darkMode.value = !darkMode.value
  document.documentElement.dataset.theme = darkMode.value ? 'dark' : 'light'
}

onMounted(() => {
  darkMode.value = window.matchMedia('(prefers-color-scheme: dark)').matches
  document.documentElement.dataset.theme = darkMode.value ? 'dark' : 'light'
  clock = window.setInterval(() => { now.value = new Date() }, 30_000)
  void loadLatestAgentVersion()
})
onBeforeUnmount(() => window.clearInterval(clock))
</script>

<template>
  <div class="app-shell">
    <aside :class="['sidebar', { open: mobileNav }]">
      <div class="sidebar-brand">
        <div class="brand-mark small" aria-hidden="true"><span>A</span></div>
        <div><strong>Ace IT Center</strong><small>CONTROL PLANE</small></div>
      </div>
      <nav aria-label="主导航">
        <a href="#nodes" :class="{ active: activeView === 'overview' }" :aria-current="activeView === 'overview' ? 'page' : undefined" @click.prevent="showView('overview')">
          <DataBoard aria-hidden="true" /><span>设备总览</span><b>{{ nodes.length }}</b>
        </a>
        <a href="#network" :class="{ active: activeView === 'network' }" :aria-current="activeView === 'network' ? 'page' : undefined" @click.prevent="showView('network')">
          <Connection aria-hidden="true" /><span>网络监控</span>
        </a>
        <a href="#commands" :class="{ active: activeView === 'commands' }" :aria-current="activeView === 'commands' ? 'page' : undefined" @click.prevent="showView('commands')">
          <Monitor aria-hidden="true" /><span>命令中心</span>
        </a>
        <a href="#pairings" :class="{ active: activeView === 'pairings' }" :aria-current="activeView === 'pairings' ? 'page' : undefined" @click.prevent="showView('pairings')">
          <Connection aria-hidden="true" /><span>待配对设备</span><b v-if="pendingPairingCount">{{ pendingPairingCount }}</b>
        </a>
        <a href="#downloads" :class="{ active: activeView === 'downloads' }" :aria-current="activeView === 'downloads' ? 'page' : undefined" @click.prevent="showView('downloads')">
          <Download aria-hidden="true" /><span>客户端下载</span>
        </a>
        <a href="#updates" :class="{ active: activeView === 'updates' }" :aria-current="activeView === 'updates' ? 'page' : undefined" @click.prevent="showView('updates')">
          <UploadFilled aria-hidden="true" /><span>系统升级</span>
        </a>
      </nav>
      <div class="sidebar-spacer"></div>
      <div class="sidebar-health">
        <span class="status-light"></span>
        <div><strong>系统运行中</strong><small>{{ onlineCount }} 台设备在线</small></div>
      </div>
      <button class="sidebar-user" type="button" title="退出登录" @click="emit('logout')">
        <span>{{ owner.username.slice(0, 1).toUpperCase() }}</span>
        <div><strong>{{ owner.username }}</strong><small>OWNER</small></div>
        <SwitchButton aria-hidden="true" />
      </button>
      <dl class="sidebar-versions" aria-label="版本信息">
        <div data-version="web" title="当前 Web 版本"><dt>WEB</dt><dd>V{{ webVersion }}</dd></div>
        <div data-version="agent" title="最新 Agent 版本"><dt>AGENT</dt><dd>{{ displayedAgentVersion }}</dd></div>
      </dl>
    </aside>

    <div v-if="mobileNav" class="nav-scrim" @click="mobileNav = false"></div>

    <main class="workspace">
      <header class="topbar">
        <button class="icon-button mobile-only" type="button" title="打开导航" @click="mobileNav = !mobileNav">
          <Fold v-if="mobileNav" /><Expand v-else />
        </button>
        <div>
          <p class="section-index">{{ pageTitle.index }}</p>
          <h1>{{ pageTitle.title }}</h1>
        </div>
        <div class="topbar-actions">
          <button v-if="activeView === 'overview' || activeView === 'network' || activeView === 'pairings'" class="icon-button" type="button" title="刷新数据" @click="refreshWorkspace"><Refresh /></button>
          <button class="icon-button" type="button" :title="darkMode ? '切换浅色模式' : '切换深色模式'" @click="toggleTheme">
            <Sunny v-if="darkMode" /><Moon v-else />
          </button>
          <button class="icon-button desktop-only" type="button" title="通知"><Bell /></button>
          <button v-if="activeView === 'overview'" class="primary-button compact" type="button" @click="showView('pairings')">
            <Plus />添加设备
          </button>
        </div>
      </header>

      <template v-if="activeView === 'overview'">
        <section class="metric-band" aria-label="设备状态汇总">
          <article><span>节点总数</span><strong>{{ nodes.length }}</strong><small><Connection />已纳管设备</small></article>
          <article><span>在线</span><strong class="success">{{ onlineCount }}</strong><small><i class="metric-dot online"></i>90 秒内有心跳</small></article>
          <article><span>离线</span><strong :class="{ danger: offlineCount > 0 }">{{ offlineCount }}</strong><small><i class="metric-dot offline"></i>需要检查</small></article>
          <article><span>待配对</span><strong :class="{ danger: pendingPairingCount > 0 }">{{ pendingPairingCount }}</strong><small><Connection />等待批准</small></article>
        </section>

        <NodeTable id="nodes" :groups="groups" :nodes="nodes" :now="now" @updated="emit('refresh')" />
      </template>
      <NetworkMonitoring v-else-if="activeView === 'network'" data-testid="network-monitoring" :nodes="nodes" :now="now" />
      <CommandCenter v-else-if="activeView === 'commands'" :nodes="nodes" />
      <PairingRequests v-else-if="activeView === 'pairings'" :pairings="pairings" :groups="groups" @approved="refreshWorkspace" @rejected="refreshWorkspace" @create-group="openDialog" />
      <AgentDownloads v-else-if="activeView === 'downloads'" />
      <SystemUpdate v-else @session-expired="emit('session-expired')" />

      <footer class="workspace-footer">
        <span>Ace IT Center / Phase 1</span><span>Last refresh {{ now.toLocaleTimeString('zh-CN', { hour12: false }) }}</span>
      </footer>
    </main>

    <ElDialog v-model="dialogOpen" title="新建设备分组" width="min(520px, calc(100vw - 32px))" align-center>
      <form class="dialog-form" @submit.prevent="submitDialog">
        <label for="group-name">分组名称</label>
        <input id="group-name" v-model="form.name" required autofocus>
        <p v-if="actionError" class="form-error" role="alert">{{ actionError }}</p>
        <div class="dialog-actions">
          <button class="secondary-button" type="button" @click="closeDialog">关闭</button>
          <button class="primary-button" type="submit" :disabled="actionBusy">{{ actionBusy ? '处理中' : '创建' }}</button>
        </div>
      </form>
    </ElDialog>
  </div>
</template>
