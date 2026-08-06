<script setup lang="ts">
import { computed, ref } from 'vue'
import { Box, Monitor } from '@element-plus/icons-vue'
import { isNodeOnline } from '../lib/status'
import { beijingUsageDay, formatTrafficBytes } from '../lib/traffic'
import type { Node } from '../types'
import NetworkHistoryDrawer from './NetworkHistoryDrawer.vue'

const props = defineProps<{
  nodes: Node[]
  now: Date
}>()

const selectedNode = ref<Node | null>(null)
const drawerOpen = ref(false)
const capableNodes = computed(() => props.nodes.filter(node => node.network_metrics_available))
const onlineCapableNodes = computed(() => capableNodes.value.filter(node => isNodeOnline(node.last_seen_at, props.now)))
const totalDownload = computed(() => onlineCapableNodes.value.reduce((total, node) => total + finiteRate(node.network_download_mb_s), 0))
const totalUpload = computed(() => onlineCapableNodes.value.reduce((total, node) => total + finiteRate(node.network_upload_mb_s), 0))
const onlineCapableCount = computed(() => onlineCapableNodes.value.length)
const currentUsageDay = computed(() => beijingUsageDay(props.now))
const currentUsageMonth = computed(() => currentUsageDay.value.slice(0, 7))

function finiteRate(value: number): number {
  return Number.isFinite(value) && value >= 0 ? value : 0
}

function rate(value: number): string {
  return `${finiteRate(value).toFixed(2)} MB/s`
}

function currentRate(node: Node, value: number): string {
  if (!node.network_metrics_available || !isNodeOnline(node.last_seen_at, props.now)) return '-'
  return rate(value)
}

function todayUsage(node: Node, value: number | undefined): string {
  if (!node.network_usage_available || typeof value !== 'number') return '-'
  if (node.network_usage_day !== currentUsageDay.value) return '0 B'
  return formatTrafficBytes(value)
}

function monthUsage(node: Node, value: number | undefined): string {
  if (!node.network_usage_available || typeof value !== 'number') return '-'
  if (node.network_usage_day?.slice(0, 7) !== currentUsageMonth.value) return '0 B'
  return formatTrafficBytes(value)
}

function openHistory(node: Node) {
  if (!node.network_metrics_available) return
  selectedNode.value = node
  drawerOpen.value = true
}

function handleRowKey(event: KeyboardEvent, node: Node) {
  if (event.key !== 'Enter' && event.key !== ' ') return
  event.preventDefault()
  openHistory(node)
}
</script>

<template>
  <section class="network-monitoring" aria-labelledby="network-monitoring-title">
    <header class="network-monitoring-heading">
      <div>
        <p class="section-index">NETWORK / TRAFFIC</p>
        <h2 id="network-monitoring-title">网络监控</h2>
      </div>
    </header>

    <div class="network-metric-band">
      <article data-metric="download-total">
        <span>当前下载总量</span>
        <strong>{{ rate(totalDownload) }}</strong>
        <small>在线采集设备合计</small>
      </article>
      <article data-metric="upload-total">
        <span>当前上传总量</span>
        <strong>{{ rate(totalUpload) }}</strong>
        <small>在线采集设备合计</small>
      </article>
      <article data-metric="capable-online">
        <span>在线采集设备</span>
        <strong>{{ onlineCapableCount }} / {{ capableNodes.length }}</strong>
        <small>90 秒在线阈值</small>
      </article>
    </div>

    <div v-if="nodes.length" class="table-wrap network-table-wrap">
      <table class="network-table">
        <thead>
          <tr>
            <th>设备</th>
            <th>状态</th>
            <th>当前下载</th>
            <th>当前上传</th>
            <th>今日总下载</th>
            <th>今日总上传</th>
            <th>本月总下载</th>
            <th>本月总上传</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="node in nodes"
            :key="node.id"
            :data-node-id="node.id"
            :class="{ 'history-capable': node.network_metrics_available }"
            :tabindex="node.network_metrics_available ? 0 : undefined"
            :aria-label="node.network_metrics_available ? `查看 ${node.name} 网络历史` : undefined"
            @click="openHistory(node)"
            @keydown="handleRowKey($event, node)"
          >
            <td data-label="设备">
              <div class="node-identity">
                <span class="node-type-icon" aria-hidden="true"><Monitor v-if="node.type === 'windows'" /><Box v-else /></span>
                <div><strong>{{ node.name }}</strong><small>{{ node.ip_address || '无 IP 地址' }}</small></div>
              </div>
            </td>
            <td data-label="状态">
              <span :class="['status-chip', isNodeOnline(node.last_seen_at, now) ? 'online' : 'offline']">
                <i></i>{{ isNodeOnline(node.last_seen_at, now) ? '在线' : '离线' }}
              </span>
            </td>
            <td data-label="当前下载" class="mono">{{ currentRate(node, node.network_download_mb_s) }}</td>
            <td data-label="当前上传" class="mono">{{ currentRate(node, node.network_upload_mb_s) }}</td>
            <td data-label="今日总下载" class="mono">{{ todayUsage(node, node.network_today_download_bytes) }}</td>
            <td data-label="今日总上传" class="mono">{{ todayUsage(node, node.network_today_upload_bytes) }}</td>
            <td data-label="本月总下载" class="mono">{{ monthUsage(node, node.network_month_download_bytes) }}</td>
            <td data-label="本月总上传" class="mono">{{ monthUsage(node, node.network_month_upload_bytes) }}</td>
          </tr>
        </tbody>
      </table>
    </div>
    <div v-else class="empty-state network-empty">
      <Box aria-hidden="true" />
      <strong>尚未接入设备</strong>
    </div>

    <NetworkHistoryDrawer
      v-if="selectedNode"
      v-model="drawerOpen"
      :node="selectedNode"
    />
  </section>
</template>
