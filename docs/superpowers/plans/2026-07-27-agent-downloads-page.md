# Ace Agent Client Downloads Page Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a logged-in Ace Agent downloads view that identifies Windows and Linux packages, links to the existing binaries, and opens the existing enrollment flow.

**Architecture:** Add one presentation-only `AgentDownloads` component and keep workspace navigation state in `OperationsWorkspace`. Reuse the existing Enrollment Token dialog through an emitted event, use same-origin relative download URLs, and avoid backend, database, router, and Agent changes.

**Tech Stack:** Vue 3 Composition API, TypeScript, Element Plus icons, Vitest, Vue Test Utils, CSS custom properties, Vite, Docker Compose on DSM.

## Global Constraints

- The downloads view is available only after Owner authentication.
- Support exactly Windows x64 and Linux x64 packages already served from `/downloads/`.
- Use `/downloads/AceAgent-windows-amd64.exe` and `/downloads/ace-agent-linux-amd64` as same-origin relative URLs.
- State that Ace Agent handles enrollment, inventory, resource state, and heartbeat reporting.
- State that MeshCentral Agent is a separate client for remote desktop, terminal, and file operations.
- Reuse the existing Enrollment Token dialog and disable enrollment when no group exists.
- Do not add Vue Router, backend endpoints, database changes, installers, service registration, or MeshCentral downloads.
- Preserve the existing IBM Plex typography, 4 px radius, light/dark tokens, mobile sidebar, and reduced-motion behavior.
- Use Simplified Chinese UI copy and Element Plus icons; do not add custom SVG icons.
- Follow Red-Green-Refactor for every behavior change and commit only files owned by each task.

---

## File Map

- Create `frontend/src/components/AgentDownloads.vue`: static platform metadata, download links, role notes, and enrollment event.
- Create `frontend/src/components/AgentDownloads.test.ts`: component rendering, URL, event, and disabled-state coverage.
- Create `frontend/src/components/OperationsWorkspace.test.ts`: workspace view switching, active navigation, return path, and mobile drawer behavior.
- Modify `frontend/src/components/OperationsWorkspace.vue`: local view state, sidebar navigation, conditional workspace content, and enrollment event wiring.
- Modify `frontend/src/style.css`: responsive platform rows and role-description layout using existing tokens.
- Deploy the changed frontend files under `/volume4/docker/docker/ace-it-center/frontend/` and rebuild only the DSM `web` service.

### Task 1: Build the Agent Downloads Component

**Files:**
- Create: `frontend/src/components/AgentDownloads.test.ts`
- Create: `frontend/src/components/AgentDownloads.vue`
- Modify: `frontend/src/style.css`

**Interfaces:**
- Consumes: `canEnroll: boolean` prop.
- Produces: `enroll` event with no payload.
- Produces: same-origin links `/downloads/AceAgent-windows-amd64.exe` and `/downloads/ace-agent-linux-amd64`.

- [ ] **Step 1: Write the failing component test**

Create `frontend/src/components/AgentDownloads.test.ts`:

```ts
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import AgentDownloads from './AgentDownloads.vue'

describe('AgentDownloads', () => {
  it('lists the supported platforms and exact same-origin downloads', () => {
    const wrapper = mount(AgentDownloads, { props: { canEnroll: true } })

    expect(wrapper.text()).toContain('Windows x64')
    expect(wrapper.text()).toContain('Linux x64')
    expect(wrapper.text()).toContain('Ace Agent')
    expect(wrapper.text()).toContain('MeshCentral Agent')

    const windows = wrapper.get('a[href="/downloads/AceAgent-windows-amd64.exe"]')
    const linux = wrapper.get('a[href="/downloads/ace-agent-linux-amd64"]')
    expect(windows.attributes('download')).toBe('AceAgent-windows-amd64.exe')
    expect(linux.attributes('download')).toBe('ace-agent-linux-amd64')
  })

  it('requests enrollment only when a target group is available', async () => {
    const wrapper = mount(AgentDownloads, { props: { canEnroll: true } })
    const enrollButton = wrapper.get('button[data-action="enroll"]')

    await enrollButton.trigger('click')
    expect(wrapper.emitted('enroll')).toHaveLength(1)

    await wrapper.setProps({ canEnroll: false })
    expect(enrollButton.attributes('disabled')).toBeDefined()
  })
})
```

- [ ] **Step 2: Run the test and verify RED**

Run:

```bash
cd frontend
npm test -- src/components/AgentDownloads.test.ts
```

Expected: FAIL because `AgentDownloads.vue` does not exist.

- [ ] **Step 3: Implement the minimal component**

Create `frontend/src/components/AgentDownloads.vue`:

```vue
<script setup lang="ts">
import { Cpu, Download, Key, Monitor } from '@element-plus/icons-vue'

defineProps<{ canEnroll: boolean }>()
const emit = defineEmits<{ enroll: [] }>()

const agents = [
  {
    platform: 'Windows x64',
    fileName: 'AceAgent-windows-amd64.exe',
    href: '/downloads/AceAgent-windows-amd64.exe',
    icon: Monitor,
  },
  {
    platform: 'Linux x64',
    fileName: 'ace-agent-linux-amd64',
    href: '/downloads/ace-agent-linux-amd64',
    icon: Cpu,
  },
]
</script>

<template>
  <section class="downloads-section" aria-labelledby="agent-downloads-title">
    <header class="downloads-intro">
      <p class="section-index">02 / AGENTS</p>
      <h2 id="agent-downloads-title">选择客户端平台</h2>
      <p>下载与设备系统匹配的 Ace Agent，接入后由平台持续接收设备状态。</p>
    </header>

    <div class="agent-download-list">
      <article v-for="agent in agents" :key="agent.platform" class="agent-download-row">
        <span class="platform-symbol" aria-hidden="true"><component :is="agent.icon" /></span>
        <div class="agent-platform"><strong>{{ agent.platform }}</strong><small>AMD64 / x86-64</small></div>
        <code class="agent-file-name">{{ agent.fileName }}</code>
        <a class="primary-button download-button" :href="agent.href" :download="agent.fileName">
          <Download aria-hidden="true" />下载
        </a>
      </article>
    </div>

    <dl class="agent-role-list">
      <div><dt>Ace Agent</dt><dd>设备注册、基础资产采集、资源状态和心跳上报。</dd></div>
      <div><dt>MeshCentral Agent</dt><dd>远程桌面、终端和文件操作使用的独立客户端，后续接入。</dd></div>
    </dl>

    <div class="download-enrollment">
      <div><Key aria-hidden="true" /><span><strong>准备接入设备</strong><small>下载后生成一次性 Enrollment Token。</small></span></div>
      <button
        class="primary-button"
        data-action="enroll"
        type="button"
        :disabled="!canEnroll"
        @click="emit('enroll')"
      >
        <Key aria-hidden="true" />生成接入令牌
      </button>
    </div>
  </section>
</template>
```

- [ ] **Step 4: Run the focused test and verify GREEN**

Run:

```bash
cd frontend
npm test -- src/components/AgentDownloads.test.ts
```

Expected: 2 tests PASS.

- [ ] **Step 5: Add responsive styles without changing behavior**

Append the following blocks before the existing media queries in `frontend/src/style.css`:

```css
.downloads-section { padding: 28px 0; border-bottom: 1px solid var(--line); }
.downloads-intro { max-width: 680px; margin-bottom: 24px; }
.downloads-intro h2 { margin-top: 6px; }
.downloads-intro > p:last-child { margin-top: 10px; color: var(--muted); font-size: 14px; line-height: 1.6; }
.agent-download-list { border-top: 1px solid var(--line); }
.agent-download-row { display: grid; grid-template-columns: 44px minmax(0, 1fr); gap: 12px; align-items: center; padding: 16px 0; border-bottom: 1px solid var(--line); }
.platform-symbol { display: grid; place-items: center; width: 40px; height: 40px; color: var(--accent); background: var(--surface-alt); border-radius: var(--radius); }
.agent-platform strong, .agent-platform small { display: block; }
.agent-platform strong { font-size: 14px; }
.agent-platform small { margin-top: 4px; color: var(--muted); font-size: 11px; }
.agent-file-name { grid-column: 1 / -1; min-width: 0; color: var(--muted); overflow-wrap: anywhere; font-size: 11px; }
.download-button { grid-column: 1 / -1; width: 100%; text-decoration: none; }
.agent-role-list { margin: 24px 0 0; border-top: 1px solid var(--line); }
.agent-role-list > div { padding: 15px 0; border-bottom: 1px solid var(--line); }
.agent-role-list dt { font-weight: 600; }
.agent-role-list dd { margin: 6px 0 0; color: var(--muted); font-size: 13px; line-height: 1.6; }
.download-enrollment { display: grid; gap: 16px; align-items: center; margin-top: 24px; padding: 18px; background: var(--surface); border: 1px solid var(--line); border-radius: var(--radius); }
.download-enrollment > div { display: flex; align-items: center; gap: 12px; }
.download-enrollment > div > svg { color: var(--accent); }
.download-enrollment strong, .download-enrollment small { display: block; }
.download-enrollment small { margin-top: 4px; color: var(--muted); font-size: 12px; }
```

Add these rules inside the existing `@media (min-width: 680px)` block:

```css
.agent-download-row { grid-template-columns: 44px minmax(130px, .75fr) minmax(0, 1.25fr) auto; }
.agent-file-name, .download-button { grid-column: auto; }
.download-button { width: auto; }
.agent-role-list { display: grid; grid-template-columns: 1fr 1fr; }
.agent-role-list > div { padding-right: 24px; }
.agent-role-list > div + div { padding-left: 24px; border-left: 1px solid var(--line); }
.download-enrollment { grid-template-columns: 1fr auto; }
```

- [ ] **Step 6: Re-run the focused test**

Run:

```bash
cd frontend
npm test -- src/components/AgentDownloads.test.ts
```

Expected: 2 tests PASS after styling.

- [ ] **Step 7: Commit Task 1**

```bash
git add frontend/src/components/AgentDownloads.vue frontend/src/components/AgentDownloads.test.ts frontend/src/style.css
git commit -m "feat: 添加 Agent 客户端下载组件"
```

### Task 2: Integrate Downloads Navigation Into the Workspace

**Files:**
- Create: `frontend/src/components/OperationsWorkspace.test.ts`
- Modify: `frontend/src/components/OperationsWorkspace.vue`

**Interfaces:**
- Consumes from Task 1: `<AgentDownloads :can-enroll="boolean" @enroll="handler" />`.
- Produces: `WorkspaceView = 'overview' | 'downloads'` local state.
- Preserves: existing `refresh` and `logout` emits, props, dialog API calls, and Enrollment Token behavior.

- [ ] **Step 1: Write failing workspace navigation tests**

Create `frontend/src/components/OperationsWorkspace.test.ts`:

```ts
import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import OperationsWorkspace from './OperationsWorkspace.vue'

function mountWorkspace() {
  vi.stubGlobal('matchMedia', vi.fn(() => ({ matches: false })))
  return mount(OperationsWorkspace, {
    props: {
      owner: { id: 'owner-1', username: 'jarvis' },
      organizations: [],
      sites: [],
      groups: [],
      nodes: [],
    },
  })
}

describe('OperationsWorkspace', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('switches between overview and client downloads with accurate active navigation', async () => {
    const wrapper = mountWorkspace()
    const overview = wrapper.get('a[href="#nodes"]')
    const downloads = wrapper.get('a[href="#downloads"]')

    expect(overview.attributes('aria-current')).toBe('page')
    await downloads.trigger('click')

    expect(downloads.attributes('aria-current')).toBe('page')
    expect(wrapper.text()).toContain('选择客户端平台')
    expect(wrapper.find('.metric-band').exists()).toBe(false)

    await overview.trigger('click')
    expect(overview.attributes('aria-current')).toBe('page')
    expect(wrapper.find('.metric-band').exists()).toBe(true)
    wrapper.unmount()
  })

  it('closes the mobile navigation after selecting downloads', async () => {
    const wrapper = mountWorkspace()

    await wrapper.get('button[title="打开导航"]').trigger('click')
    expect(wrapper.get('aside').classes()).toContain('open')

    await wrapper.get('a[href="#downloads"]').trigger('click')
    expect(wrapper.get('aside').classes()).not.toContain('open')
    wrapper.unmount()
  })
})
```

- [ ] **Step 2: Run the test and verify RED**

Run:

```bash
cd frontend
npm test -- src/components/OperationsWorkspace.test.ts
```

Expected: FAIL because the `#downloads` navigation item and view state do not exist.

- [ ] **Step 3: Add imports and view state**

Update `frontend/src/components/OperationsWorkspace.vue`:

```ts
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import {
  Bell, Connection, DataBoard, Download, Expand, Fold, Key, Location, Moon,
  OfficeBuilding, Plus, Refresh, Sunny, SwitchButton,
} from '@element-plus/icons-vue'
import AgentDownloads from './AgentDownloads.vue'

type WorkspaceView = 'overview' | 'downloads'

const activeView = ref<WorkspaceView>('overview')

function showView(view: WorkspaceView) {
  activeView.value = view
  mobileNav.value = false
}

async function showStructure() {
  showView('overview')
  await nextTick()
  document.getElementById('structure')?.scrollIntoView?.()
}
```

The replacement import removes the existing unused `Setting` icon.

- [ ] **Step 4: Replace static sidebar active state with view-aware navigation**

Replace the three current sidebar navigation anchors with:

```vue
<a href="#nodes" :class="{ active: activeView === 'overview' }" :aria-current="activeView === 'overview' ? 'page' : undefined" @click.prevent="showView('overview')">
  <DataBoard aria-hidden="true" /><span>设备总览</span><b>{{ nodes.length }}</b>
</a>
<a href="#structure" @click.prevent="showStructure"><OfficeBuilding aria-hidden="true" /><span>组织结构</span></a>
<a href="#downloads" :class="{ active: activeView === 'downloads' }" :aria-current="activeView === 'downloads' ? 'page' : undefined" @click.prevent="showView('downloads')">
  <Download aria-hidden="true" /><span>客户端下载</span>
</a>
<a href="#enrollment" @click.prevent="openDialog('enrollment')"><Key aria-hidden="true" /><span>设备接入</span></a>
```

- [ ] **Step 5: Make the topbar and workspace content view-aware**

Use these expressions in the existing topbar:

```vue
<p class="section-index">{{ activeView === 'overview' ? 'INFRASTRUCTURE / OVERVIEW' : 'INFRASTRUCTURE / AGENTS' }}</p>
<h1>{{ activeView === 'overview' ? '设备运行总览' : '客户端下载' }}</h1>
```

Show the refresh and topbar “添加设备” actions only for `overview`, while keeping theme and notification controls available in both views:

```vue
<button v-if="activeView === 'overview'" class="icon-button" type="button" title="刷新数据" @click="emit('refresh')"><Refresh /></button>
<!-- existing theme and notification buttons remain -->
<button v-if="activeView === 'overview'" class="primary-button compact" type="button" :disabled="groups.length === 0" @click="openDialog('enrollment')">
  <Plus />添加设备
</button>
```

Wrap the metric band, organization section, and `NodeTable` in an overview template, then render downloads as the alternative:

```vue
<template v-if="activeView === 'overview'">
  <!-- existing metric-band -->
  <!-- existing structure-section -->
  <NodeTable id="nodes" :nodes="nodes" :now="now" />
</template>
<AgentDownloads v-else :can-enroll="groups.length > 0" @enroll="openDialog('enrollment')" />
```

Do not move or duplicate the existing `ElDialog`; both views continue to use the same dialog instance.

- [ ] **Step 6: Run the focused workspace and download tests**

Run:

```bash
cd frontend
npm test -- src/components/OperationsWorkspace.test.ts src/components/AgentDownloads.test.ts
```

Expected: 4 tests PASS.

- [ ] **Step 7: Run all frontend tests**

Run:

```bash
cd frontend
npm test
```

Expected: all existing and new test files PASS with no unhandled errors.

- [ ] **Step 8: Commit Task 2**

```bash
git add frontend/src/components/OperationsWorkspace.vue frontend/src/components/OperationsWorkspace.test.ts
git commit -m "feat: 集成客户端下载工作台入口"
```

### Task 3: Verify, Deploy to DSM, and Validate the Live Route

**Files:**
- Verify: all tracked changes from Tasks 1 and 2.
- Deploy: `frontend/src/components/AgentDownloads.vue`, `frontend/src/components/AgentDownloads.test.ts`, `frontend/src/components/OperationsWorkspace.vue`, `frontend/src/components/OperationsWorkspace.test.ts`, `frontend/src/style.css`.
- Remote target: `/volume4/docker/docker/ace-it-center/frontend/`.

**Interfaces:**
- Consumes: committed frontend implementation from Tasks 1 and 2.
- Produces: rebuilt `ace-it-center-web` image and healthy `web` container on DSM.

- [ ] **Step 1: Run repository checks and production build**

Run:

```bash
git diff --check
cd frontend
npm test
npm run build
```

Expected: `git diff --check` exits 0, all tests pass, and Vite reports a successful production build.

- [ ] **Step 2: Inspect the final local change set**

Run:

```bash
git status --short
git log -3 --oneline
```

Expected: only intentional plan or implementation files appear, and both implementation commits are present.

- [ ] **Step 3: Back up the remote frontend files**

Connect to DSM using the existing SSH endpoint and create a dated backup inside the project backup directory:

```bash
sudo mkdir -p /volume4/docker/docker/ace-it-center/backup/frontend-before-downloads-page-20260727
sudo cp -a /volume4/docker/docker/ace-it-center/frontend/src/components/OperationsWorkspace.vue /volume4/docker/docker/ace-it-center/backup/frontend-before-downloads-page-20260727/
sudo cp -a /volume4/docker/docker/ace-it-center/frontend/src/style.css /volume4/docker/docker/ace-it-center/backup/frontend-before-downloads-page-20260727/
```

- [ ] **Step 4: Upload only the owned frontend files**

Use legacy SCP because DSM does not provide the newer SFTP subsystem:

```bash
scp -O -P 9099 frontend/src/components/AgentDownloads.vue frontend/src/components/AgentDownloads.test.ts frontend/src/components/OperationsWorkspace.vue frontend/src/components/OperationsWorkspace.test.ts jarvis@ace-station.top:/volume4/docker/docker/ace-it-center/frontend/src/components/
scp -O -P 9099 frontend/src/style.css jarvis@ace-station.top:/volume4/docker/docker/ace-it-center/frontend/src/style.css
```

- [ ] **Step 5: Rebuild and recreate only the web service**

On DSM:

```bash
cd /volume4/docker/docker/ace-it-center/deploy
sudo docker compose build web
sudo docker compose up -d --no-deps --force-recreate web
```

Expected: the `web` image builds successfully and only `ace-it-center-web-1` is recreated.

- [ ] **Step 6: Verify container health and public downloads**

On DSM:

```bash
sudo docker compose ps
curl --noproxy '*' -fsS -H 'Host: it.ace-station.top:1111' http://127.0.0.1:1111/api/v1/health
curl --noproxy '*' -fsSI -H 'Host: it.ace-station.top:1111' http://127.0.0.1:1111/downloads/AceAgent-windows-amd64.exe
curl --noproxy '*' -fsSI -H 'Host: it.ace-station.top:1111' http://127.0.0.1:1111/downloads/ace-agent-linux-amd64
```

Expected: all three services are healthy, the API returns `{"status":"ok"}`, and both downloads return `HTTP/1.1 200 OK` with `Content-Type: application/octet-stream`.

- [ ] **Step 7: Verify the deployed web bundle contains the feature**

On DSM:

```bash
sudo docker exec ace-it-center-web-1 grep -R -l 'AceAgent-windows-amd64.exe' /usr/share/nginx/html/assets
sudo docker exec ace-it-center-web-1 grep -R -l 'MeshCentral Agent' /usr/share/nginx/html/assets
```

Expected: both commands print the current JavaScript asset path.

- [ ] **Step 8: Verify the public route from the development machine**

Run:

```bash
curl --noproxy '*' -fsS --max-time 20 http://it.ace-station.top:1111/api/v1/health
curl --noproxy '*' -fsSI --max-time 20 http://it.ace-station.top:1111/downloads/AceAgent-windows-amd64.exe
curl --noproxy '*' -fsSI --max-time 20 http://it.ace-station.top:1111/downloads/ace-agent-linux-amd64
```

Expected: the health endpoint and both binary downloads are reachable through the public reverse proxy.

- [ ] **Step 9: Final working-tree and commit verification**

Run:

```bash
git status --short
git log -3 --oneline --decorate
```

Expected: the worktree is clean and the design, component, and workspace commits are visible.
