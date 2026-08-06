<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Cpu, Download, Monitor } from '@element-plus/icons-vue'

const releaseManifestPath = '/downloads/windows/stable/latest.json'
const fallbackWindowsVersion = '0.3.8'
const windowsVersion = ref(fallbackWindowsVersion)
const windowsInstallerPath = ref(installerPath(fallbackWindowsVersion))

function installerPath(version: string): string {
  return `/downloads/windows/stable/AceAgentSetup-windows-amd64-V${version}.exe`
}

function isReleaseVersion(value: unknown): value is string {
  return typeof value === 'string' && /^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(value)
}

async function loadLatestWindowsRelease() {
  try {
    const response = await fetch(releaseManifestPath, { credentials: 'same-origin' })
    if (!response.ok) return

    const manifest = await response.json() as { version?: unknown; url?: unknown }
    if (!isReleaseVersion(manifest.version)) return

    const expectedPath = installerPath(manifest.version)
    if (manifest.url !== expectedPath) return

    windowsVersion.value = manifest.version
    windowsInstallerPath.value = expectedPath
  } catch {
    // 下载清单不可用时保留已发布的稳定版本链接。
  }
}

const agents = computed(() => [
  {
    platform: 'Windows 10/11 x64',
    architecture: 'AMD64 / 64-bit installer',
    note: '安装后从系统托盘打开 Ace Agent，填写服务器地址，再回到平台的待配对设备确认。',
    fileName: `AceAgentSetup-windows-amd64-V${windowsVersion.value}.exe`,
    href: windowsInstallerPath.value,
    icon: Monitor,
  },
  {
    platform: 'Linux x64',
    architecture: 'AMD64 / x86-64',
    note: '下载后授权执行，在终端使用 sudo 启动 Agent。',
    fileName: 'ace-agent-linux-amd64',
    href: '/downloads/ace-agent-linux-amd64',
    icon: Cpu,
  },
])

onMounted(() => { void loadLatestWindowsRelease() })
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
        <div class="agent-platform">
          <strong>{{ agent.platform }}</strong>
          <small>{{ agent.architecture }}</small>
          <p>{{ agent.note }}</p>
        </div>
        <code class="agent-file-name">{{ agent.fileName }}</code>
        <a
          class="primary-button download-button"
          :href="agent.href"
          :download="agent.fileName"
          :aria-label="`下载 ${agent.platform} Ace Agent：${agent.fileName}`"
        >
          <Download aria-hidden="true" />下载
        </a>
      </article>
    </div>

    <dl class="agent-role-list">
      <div><dt>Ace Agent</dt><dd>设备注册、基础资产采集、资源状态和心跳上报。</dd></div>
      <div><dt>MeshCentral Agent</dt><dd>远程桌面、终端和文件操作使用的独立客户端，后续接入。</dd></div>
    </dl>

  </section>
</template>
