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
