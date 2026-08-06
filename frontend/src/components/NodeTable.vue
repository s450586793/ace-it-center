<script setup lang="ts">
import { computed, ref } from 'vue'
import { Box, Document, EditPen, Monitor, Search } from '@element-plus/icons-vue'
import { ElDialog } from 'element-plus'
import { APIError, apiRequest } from '../api'
import type { AgentLogSnapshot, Node, NodeGroup } from '../types'
import { isNodeOnline, networkRateLabel, percentLabel } from '../lib/status'

const props = defineProps<{
  groups: NodeGroup[]
  nodes: Node[]
  now: Date
}>()
const emit = defineEmits<{ updated: [] }>()

const groupNames = computed(() => new Map(props.groups.map(group => [group.id, group.name])))
const search = ref('')
const remarkDialogOpen = ref(false)
const editingNode = ref<Node | null>(null)
const remarkDraft = ref('')
const remarkBusy = ref(false)
const remarkError = ref('')
const logsDialogOpen = ref(false)
const logsNode = ref<Node | null>(null)
const logsSnapshot = ref<AgentLogSnapshot | null>(null)
const logsBusy = ref(false)
const logsError = ref('')
const selectedLog = ref<'agent' | 'update'>('agent')
const filteredNodes = computed(() => {
  const query = search.value.trim().toLocaleLowerCase()
  if (!query) return props.nodes
  return props.nodes.filter(node => [node.name, groupName(node.group_id), node.ip_address, node.os_name, node.type]
    .some(value => value.toLocaleLowerCase().includes(query)))
})

function groupName(groupID: string): string {
  return groupNames.value.get(groupID) || '-'
}

function lastSeenLabel(value: string | null): string {
  if (!value) return '从未'
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false,
  }).format(new Date(value))
}

function livePercentLabel(node: Node, value: number): string {
  if (!isNodeOnline(node.last_seen_at, props.now)) return '-'
  return percentLabel(value)
}

function openRemarkEditor(node: Node) {
  editingNode.value = node
  remarkDraft.value = node.remark || ''
  remarkError.value = ''
  remarkDialogOpen.value = true
}

function closeRemarkEditor() {
  remarkDialogOpen.value = false
}

async function openLogViewer(node: Node) {
  logsNode.value = node
  logsSnapshot.value = null
  logsError.value = ''
  selectedLog.value = 'agent'
  logsDialogOpen.value = true
  logsBusy.value = true
  try {
    logsSnapshot.value = await apiRequest<AgentLogSnapshot>(`/api/v1/nodes/${node.id}/logs`)
  } catch (requestError) {
    logsError.value = requestError instanceof APIError && requestError.status === 404
      ? '暂无上传日志'
      : requestError instanceof Error ? requestError.message : '读取设备日志失败'
  } finally {
    logsBusy.value = false
  }
}

function logCapturedLabel(value: string): string {
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
  }).format(new Date(value))
}

async function saveRemark() {
  if (!editingNode.value) return
  remarkBusy.value = true
  remarkError.value = ''
  try {
    await apiRequest<{ node: Node }>(`/api/v1/nodes/${editingNode.value.id}`, {
      method: 'PATCH', body: JSON.stringify({ remark: remarkDraft.value }),
    })
    remarkDialogOpen.value = false
    emit('updated')
  } catch (requestError) {
    remarkError.value = requestError instanceof Error ? requestError.message : '保存备注失败'
  } finally {
    remarkBusy.value = false
  }
}
</script>

<template>
  <section class="device-section" aria-labelledby="device-table-title">
    <div class="section-toolbar">
      <div>
        <p class="section-index">02 / NODES</p>
        <h2 id="device-table-title">设备清单</h2>
      </div>
      <label class="search-field">
        <Search aria-hidden="true" />
        <span class="sr-only">搜索设备</span>
        <input v-model="search" type="search" placeholder="名称、分组、IP、系统">
      </label>
    </div>

    <div v-if="filteredNodes.length" class="table-wrap">
      <table>
        <thead>
          <tr>
            <th>设备</th>
            <th>分组</th>
            <th>状态</th>
            <th>系统</th>
            <th>IP</th>
            <th>CPU</th>
            <th>内存</th>
            <th>磁盘</th>
            <th class="network-column">网络</th>
            <th class="remark-column">备注</th>
            <th>最后在线</th>
            <th class="action-column">操作</th>
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
            <td data-label="分组">{{ groupName(node.group_id) }}</td>
            <td data-label="状态">
              <span :class="['status-chip', isNodeOnline(node.last_seen_at, now) ? 'online' : 'offline']">
                <i></i>{{ isNodeOnline(node.last_seen_at, now) ? '在线' : '离线' }}
              </span>
            </td>
            <td data-label="系统"><strong>{{ node.os_name || node.type }}</strong><small>{{ node.os_version }}</small></td>
            <td data-label="IP" class="mono">{{ node.ip_address || '—' }}</td>
            <td data-label="CPU"><span class="resource-value">{{ livePercentLabel(node, node.cpu_percent) }}</span></td>
            <td data-label="内存"><span class="resource-value">{{ livePercentLabel(node, node.memory_percent) }}</span></td>
            <td data-label="磁盘"><span class="resource-value">{{ livePercentLabel(node, node.disk_percent) }}</span></td>
            <td data-label="网络" class="network-column">
              <span v-if="!isNodeOnline(node.last_seen_at, now)" class="muted">-</span>
              <span v-else-if="node.network_metrics_available" class="network-rates">
                <span>↓ {{ networkRateLabel(node.network_download_mb_s) }}</span>
                <span>↑ {{ networkRateLabel(node.network_upload_mb_s) }}</span>
              </span>
              <span v-else class="muted">待升级</span>
            </td>
            <td data-label="备注" class="remark-column">
              <div class="remark-cell">
                <span :title="node.remark || undefined">{{ node.remark || '-' }}</span>
                <button
                  class="icon-button remark-edit-button"
                  type="button"
                  title="编辑备注"
                  :aria-label="`编辑 ${node.name} 的备注`"
                  @click="openRemarkEditor(node)"
                >
                  <EditPen aria-hidden="true" />
                </button>
              </div>
            </td>
            <td data-label="最后在线" class="muted">{{ lastSeenLabel(node.last_seen_at) }}</td>
            <td data-label="操作" class="action-column">
              <button
                class="icon-button log-view-button"
                type="button"
                title="查看日志"
                :aria-label="`查看 ${node.name} 的日志`"
                @click="openLogViewer(node)"
              >
                <Document aria-hidden="true" />
              </button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <div v-else class="empty-state">
      <Box aria-hidden="true" />
      <strong>{{ nodes.length ? '没有匹配设备' : '尚未接入设备' }}</strong>
    </div>

    <ElDialog v-model="remarkDialogOpen" title="编辑设备备注" width="min(520px, calc(100vw - 32px))" align-center>
      <form class="remark-form" @submit.prevent="saveRemark">
        <label for="node-remark">{{ editingNode?.name || '设备' }}</label>
        <textarea
          id="node-remark"
          v-model="remarkDraft"
          maxlength="500"
          rows="4"
          :disabled="remarkBusy"
          placeholder="填写设备用途、位置或负责人"
          autofocus
        ></textarea>
        <p v-if="remarkError" class="form-error" role="alert">{{ remarkError }}</p>
        <div class="dialog-actions">
          <button class="secondary-button" type="button" :disabled="remarkBusy" @click="closeRemarkEditor">关闭</button>
          <button class="primary-button" type="submit" :disabled="remarkBusy">{{ remarkBusy ? '保存中' : '保存' }}</button>
        </div>
      </form>
    </ElDialog>

    <ElDialog v-model="logsDialogOpen" title="设备日志" width="min(840px, calc(100vw - 32px))" align-center>
      <div v-if="logsBusy" class="log-loading">读取日志中</div>
      <p v-else-if="logsError" class="form-error" role="alert">{{ logsError }}</p>
      <div v-else-if="logsSnapshot" class="log-viewer">
        <div class="log-viewer-head">
          <strong>{{ logsNode?.name }}</strong>
          <span>上传于 {{ logCapturedLabel(logsSnapshot.captured_at) }}</span>
        </div>
        <div class="log-tabs" role="tablist" aria-label="日志文件">
          <button
            class="log-tab"
            :class="{ active: selectedLog === 'agent' }"
            type="button"
            role="tab"
            :aria-selected="selectedLog === 'agent'"
            @click="selectedLog = 'agent'"
          >agent.log</button>
          <button
            class="log-tab"
            :class="{ active: selectedLog === 'update' }"
            type="button"
            role="tab"
            aria-label="查看 update.log"
            :aria-selected="selectedLog === 'update'"
            @click="selectedLog = 'update'"
          >update.log</button>
        </div>
        <pre class="log-output">{{ selectedLog === 'agent' ? logsSnapshot.agent_log : logsSnapshot.update_log }}</pre>
      </div>
    </ElDialog>
  </section>
</template>
