<script setup lang="ts">
import { computed, ref } from 'vue'
import { Box, Monitor, Search } from '@element-plus/icons-vue'
import type { Node } from '../types'
import { isNodeOnline, percentLabel } from '../lib/status'

const props = defineProps<{
  nodes: Node[]
  now: Date
}>()

const search = ref('')
const filteredNodes = computed(() => {
  const query = search.value.trim().toLocaleLowerCase()
  if (!query) return props.nodes
  return props.nodes.filter(node => [node.name, node.ip_address, node.os_name, node.type]
    .some(value => value.toLocaleLowerCase().includes(query)))
})

function lastSeenLabel(value: string | null): string {
  if (!value) return '从未'
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false,
  }).format(new Date(value))
}
</script>

<template>
  <section class="device-section" aria-labelledby="device-table-title">
    <div class="section-toolbar">
      <div>
        <p class="section-index">03 / NODES</p>
        <h2 id="device-table-title">设备清单</h2>
      </div>
      <label class="search-field">
        <Search aria-hidden="true" />
        <span class="sr-only">搜索设备</span>
        <input v-model="search" type="search" placeholder="名称、IP、系统">
      </label>
    </div>

    <div v-if="filteredNodes.length" class="table-wrap">
      <table>
        <thead>
          <tr>
            <th>设备</th>
            <th>状态</th>
            <th>系统</th>
            <th>IP</th>
            <th>CPU</th>
            <th>内存</th>
            <th>磁盘</th>
            <th>最后在线</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="node in filteredNodes" :key="node.id">
            <td data-label="设备">
              <div class="node-identity">
                <span class="node-type-icon" aria-hidden="true">
                  <Monitor v-if="node.type === 'windows'" />
                  <Box v-else />
                </span>
                <div><strong>{{ node.name }}</strong><small>Agent {{ node.agent_version }}</small></div>
              </div>
            </td>
            <td data-label="状态">
              <span :class="['status-chip', isNodeOnline(node.last_seen_at, now) ? 'online' : 'offline']">
                <i></i>{{ isNodeOnline(node.last_seen_at, now) ? '在线' : '离线' }}
              </span>
            </td>
            <td data-label="系统"><strong>{{ node.os_name || node.type }}</strong><small>{{ node.os_version }}</small></td>
            <td data-label="IP" class="mono">{{ node.ip_address || '—' }}</td>
            <td data-label="CPU"><span class="resource-value">{{ percentLabel(node.cpu_percent) }}</span></td>
            <td data-label="内存"><span class="resource-value">{{ percentLabel(node.memory_percent) }}</span></td>
            <td data-label="磁盘"><span class="resource-value">{{ percentLabel(node.disk_percent) }}</span></td>
            <td data-label="最后在线" class="muted">{{ lastSeenLabel(node.last_seen_at) }}</td>
          </tr>
        </tbody>
      </table>
    </div>
    <div v-else class="empty-state">
      <Box aria-hidden="true" />
      <strong>{{ nodes.length ? '没有匹配设备' : '尚未接入设备' }}</strong>
    </div>
  </section>
</template>
