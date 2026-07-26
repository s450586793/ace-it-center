<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import {
  Bell, Connection, DataBoard, Download, Expand, Fold, Key, Location, Moon,
  OfficeBuilding, Plus, Refresh, Sunny, SwitchButton,
} from '@element-plus/icons-vue'
import { ElDialog } from 'element-plus'
import { apiRequest } from '../api'
import { isNodeOnline } from '../lib/status'
import type { Node, NodeGroup, Organization, Owner, Site } from '../types'
import AgentDownloads from './AgentDownloads.vue'
import NodeTable from './NodeTable.vue'

type WorkspaceView = 'overview' | 'downloads'

const props = defineProps<{
  owner: Owner
  organizations: Organization[]
  sites: Site[]
  groups: NodeGroup[]
  nodes: Node[]
}>()

const emit = defineEmits<{ refresh: []; logout: [] }>()
const now = ref(new Date())
const activeView = ref<WorkspaceView>('overview')
const mobileNav = ref(false)
const darkMode = ref(false)
const dialog = ref<'organization' | 'site' | 'group' | 'enrollment' | null>(null)
const actionBusy = ref(false)
const actionError = ref('')
const enrollmentToken = ref('')
const form = reactive({ name: '', organizationID: '', siteID: '', groupID: '', expiresMinutes: 60, maxUses: 1 })
let clock: number | undefined

const dialogOpen = computed({
  get: () => dialog.value !== null,
  set: (open: boolean) => {
    if (!open) dialog.value = null
  },
})

const onlineCount = computed(() => props.nodes.filter(node => isNodeOnline(node.last_seen_at, now.value)).length)
const offlineCount = computed(() => props.nodes.length - onlineCount.value)
const currentOrigin = computed(() => window.location.origin)
const windowsCommand = computed(() => `.\\AceAgent-windows-amd64.exe -server ${currentOrigin.value} -enrollment ${enrollmentToken.value}`)
const linuxCommand = computed(() => `chmod +x ./ace-agent-linux-amd64 && ./ace-agent-linux-amd64 -server ${currentOrigin.value} -enrollment ${enrollmentToken.value}`)

function showView(view: WorkspaceView) {
  activeView.value = view
  mobileNav.value = false
}

async function showStructure() {
  showView('overview')
  await nextTick()
  document.getElementById('structure')?.scrollIntoView?.()
}

function openDialog(type: typeof dialog.value) {
  dialog.value = type
  actionError.value = ''
  enrollmentToken.value = ''
  form.name = ''
  form.organizationID = props.organizations[0]?.id ?? ''
  form.siteID = props.sites[0]?.id ?? ''
  form.groupID = props.groups[0]?.id ?? ''
}

async function submitDialog() {
  actionBusy.value = true
  actionError.value = ''
  try {
    if (dialog.value === 'organization') {
      await apiRequest('/api/v1/organizations', { method: 'POST', body: JSON.stringify({ name: form.name }) })
    } else if (dialog.value === 'site') {
      await apiRequest('/api/v1/sites', { method: 'POST', body: JSON.stringify({ organization_id: form.organizationID, name: form.name }) })
    } else if (dialog.value === 'group') {
      await apiRequest('/api/v1/groups', { method: 'POST', body: JSON.stringify({ site_id: form.siteID, name: form.name }) })
    } else if (dialog.value === 'enrollment') {
      const result = await apiRequest<{ token: string }>('/api/v1/enrollments', {
        method: 'POST',
        body: JSON.stringify({ group_id: form.groupID, expires_minutes: form.expiresMinutes, max_uses: form.maxUses }),
      })
      enrollmentToken.value = result.token
      return
    }
    dialog.value = null
    await emit('refresh')
  } catch (requestError) {
    actionError.value = requestError instanceof Error ? requestError.message : 'Operation failed'
  } finally {
    actionBusy.value = false
  }
}

async function copyText(value: string) {
  await navigator.clipboard.writeText(value)
}

function toggleTheme() {
  darkMode.value = !darkMode.value
  document.documentElement.dataset.theme = darkMode.value ? 'dark' : 'light'
}

onMounted(() => {
  darkMode.value = window.matchMedia('(prefers-color-scheme: dark)').matches
  document.documentElement.dataset.theme = darkMode.value ? 'dark' : 'light'
  clock = window.setInterval(() => { now.value = new Date() }, 30_000)
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
        <a href="#structure" @click.prevent="showStructure"><OfficeBuilding aria-hidden="true" /><span>组织结构</span></a>
        <a href="#downloads" :class="{ active: activeView === 'downloads' }" :aria-current="activeView === 'downloads' ? 'page' : undefined" @click.prevent="showView('downloads')">
          <Download aria-hidden="true" /><span>客户端下载</span>
        </a>
        <a href="#enrollment" @click.prevent="openDialog('enrollment')"><Key aria-hidden="true" /><span>设备接入</span></a>
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
    </aside>

    <div v-if="mobileNav" class="nav-scrim" @click="mobileNav = false"></div>

    <main class="workspace">
      <header class="topbar">
        <button class="icon-button mobile-only" type="button" title="打开导航" @click="mobileNav = !mobileNav">
          <Fold v-if="mobileNav" /><Expand v-else />
        </button>
        <div>
          <p class="section-index">{{ activeView === 'overview' ? 'INFRASTRUCTURE / OVERVIEW' : 'INFRASTRUCTURE / AGENTS' }}</p>
          <h1>{{ activeView === 'overview' ? '设备运行总览' : '客户端下载' }}</h1>
        </div>
        <div class="topbar-actions">
          <button v-if="activeView === 'overview'" class="icon-button" type="button" title="刷新数据" @click="emit('refresh')"><Refresh /></button>
          <button class="icon-button" type="button" :title="darkMode ? '切换浅色模式' : '切换深色模式'" @click="toggleTheme">
            <Sunny v-if="darkMode" /><Moon v-else />
          </button>
          <button class="icon-button desktop-only" type="button" title="通知"><Bell /></button>
          <button v-if="activeView === 'overview'" class="primary-button compact" type="button" :disabled="groups.length === 0" @click="openDialog('enrollment')">
            <Plus />添加设备
          </button>
        </div>
      </header>

      <template v-if="activeView === 'overview'">
        <section class="metric-band" aria-label="设备状态汇总">
          <article><span>节点总数</span><strong>{{ nodes.length }}</strong><small><Connection />已纳管设备</small></article>
          <article><span>在线</span><strong class="success">{{ onlineCount }}</strong><small><i class="metric-dot online"></i>90 秒内有心跳</small></article>
          <article><span>离线</span><strong :class="{ danger: offlineCount > 0 }">{{ offlineCount }}</strong><small><i class="metric-dot offline"></i>需要检查</small></article>
          <article><span>地点</span><strong>{{ sites.length }}</strong><small><Location />{{ organizations.length }} 个组织</small></article>
        </section>

        <section id="structure" class="structure-section" aria-labelledby="structure-title">
          <div class="section-toolbar">
            <div><p class="section-index">02 / STRUCTURE</p><h2 id="structure-title">组织结构</h2></div>
            <div class="inline-actions">
              <button class="text-button" type="button" @click="openDialog('organization')"><Plus />组织</button>
              <button class="text-button" type="button" :disabled="organizations.length === 0" @click="openDialog('site')"><Plus />地点</button>
              <button class="text-button" type="button" :disabled="sites.length === 0" @click="openDialog('group')"><Plus />分组</button>
            </div>
          </div>
          <div v-if="organizations.length" class="structure-grid">
            <article v-for="organization in organizations" :key="organization.id" class="organization-block">
              <header><OfficeBuilding /><strong>{{ organization.name }}</strong></header>
              <div v-for="site in sites.filter(item => item.organization_id === organization.id)" :key="site.id" class="site-row">
                <span><Location />{{ site.name }}</span>
                <small>{{ groups.filter(group => group.site_id === site.id).length }} 个分组</small>
              </div>
              <p v-if="!sites.some(item => item.organization_id === organization.id)" class="muted">尚未添加地点</p>
            </article>
          </div>
          <div v-else class="structure-empty">
            <OfficeBuilding aria-hidden="true" /><strong>创建第一个组织</strong>
            <button class="text-button" type="button" @click="openDialog('organization')"><Plus />新建组织</button>
          </div>
        </section>

        <NodeTable id="nodes" :nodes="nodes" :now="now" />
      </template>
      <AgentDownloads v-else :can-enroll="groups.length > 0" @enroll="openDialog('enrollment')" />

      <footer class="workspace-footer">
        <span>Ace IT Center / Phase 1</span><span>Last refresh {{ now.toLocaleTimeString('zh-CN', { hour12: false }) }}</span>
      </footer>
    </main>

    <ElDialog v-model="dialogOpen" :title="dialog === 'enrollment' ? '添加设备' : `新建${dialog === 'organization' ? '组织' : dialog === 'site' ? '地点' : '分组'}`" width="min(520px, calc(100vw - 32px))" align-center>
      <form class="dialog-form" @submit.prevent="submitDialog">
        <template v-if="dialog === 'organization'">
          <label for="organization-name">组织名称</label>
          <input id="organization-name" v-model="form.name" required autofocus>
        </template>
        <template v-else-if="dialog === 'site'">
          <label for="site-organization">所属组织</label>
          <select id="site-organization" v-model="form.organizationID" required>
            <option v-for="item in organizations" :key="item.id" :value="item.id">{{ item.name }}</option>
          </select>
          <label for="site-name">地点名称</label><input id="site-name" v-model="form.name" required>
        </template>
        <template v-else-if="dialog === 'group'">
          <label for="group-site">所属地点</label>
          <select id="group-site" v-model="form.siteID" required>
            <option v-for="item in sites" :key="item.id" :value="item.id">{{ item.name }}</option>
          </select>
          <label for="group-name">分组名称</label><input id="group-name" v-model="form.name" required>
        </template>
        <template v-else-if="dialog === 'enrollment'">
          <template v-if="!enrollmentToken">
            <label for="enrollment-group">目标分组</label>
            <select id="enrollment-group" v-model="form.groupID" required>
              <option v-for="item in groups" :key="item.id" :value="item.id">{{ item.name }}</option>
            </select>
            <div class="split-fields">
              <div><label for="enrollment-expiry">有效分钟</label><input id="enrollment-expiry" v-model.number="form.expiresMinutes" type="number" min="1" max="10080" required></div>
              <div><label for="enrollment-uses">可用次数</label><input id="enrollment-uses" v-model.number="form.maxUses" type="number" min="1" max="100" required></div>
            </div>
          </template>
          <div v-else class="enrollment-result">
            <p class="section-index">WINDOWS</p>
            <button class="command-line" type="button" title="复制 Windows 命令" @click="copyText(windowsCommand)"><code>{{ windowsCommand }}</code><span>复制</span></button>
            <p class="section-index">LINUX</p>
            <button class="command-line" type="button" title="复制 Linux 命令" @click="copyText(linuxCommand)"><code>{{ linuxCommand }}</code><span>复制</span></button>
          </div>
        </template>
        <p v-if="actionError" class="form-error" role="alert">{{ actionError }}</p>
        <div class="dialog-actions">
          <button class="secondary-button" type="button" @click="dialog = null">关闭</button>
          <button v-if="!enrollmentToken" class="primary-button" type="submit" :disabled="actionBusy">{{ actionBusy ? '处理中' : dialog === 'enrollment' ? '生成接入令牌' : '创建' }}</button>
        </div>
      </form>
    </ElDialog>
  </div>
</template>
