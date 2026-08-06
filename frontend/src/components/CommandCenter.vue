<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { Document, Refresh, RefreshRight, VideoPlay } from '@element-plus/icons-vue'
import { ElDrawer } from 'element-plus'
import { apiRequest } from '../api'
import { isNodeOnline } from '../lib/status'
import type { CommandShell, CommandTask, CommandTaskDetail, Node } from '../types'

const props = defineProps<{ nodes: Node[] }>()

const selectedNodeIDs = ref<string[]>([])
const shell = ref<CommandShell>('powershell')
const command = ref('')
const timeoutSeconds = ref(300)
const riskConfirmed = ref(false)
const tasks = ref<CommandTask[]>([])
const detail = ref<CommandTaskDetail | null>(null)
const detailOpen = ref(false)
const loadingTasks = ref(false)
const submitBusy = ref(false)
const detailBusy = ref(false)
const retryingTaskID = ref('')
const error = ref('')
let pollTimer: number | undefined

const windowsNodes = computed(() => props.nodes.filter(node => node.type === 'windows'))
const canSubmit = computed(() => (
  selectedNodeIDs.value.length > 0 && command.value.trim() !== '' &&
  timeoutSeconds.value >= 10 && timeoutSeconds.value <= 1800 && riskConfirmed.value && !submitBusy.value
))

async function loadTasks() {
  if (loadingTasks.value) return
  loadingTasks.value = true
  try {
    const response = await apiRequest<{ items: CommandTask[] }>('/api/v1/commands')
    tasks.value = response.items
    error.value = ''
  } catch (requestError) {
    error.value = requestError instanceof Error ? requestError.message : '无法刷新命令任务'
  } finally {
    loadingTasks.value = false
  }
}

async function submitCommand() {
  if (!canSubmit.value) return
  submitBusy.value = true
  error.value = ''
  try {
    await apiRequest<CommandTaskDetail>('/api/v1/commands', {
      method: 'POST',
      body: JSON.stringify({
        node_ids: selectedNodeIDs.value,
        shell: shell.value,
        command: command.value,
        timeout_seconds: timeoutSeconds.value,
      }),
    })
    selectedNodeIDs.value = []
    riskConfirmed.value = false
    await loadTasks()
  } catch (requestError) {
    error.value = requestError instanceof Error ? requestError.message : '命令下发失败'
  } finally {
    submitBusy.value = false
  }
}

async function openDetail(task: CommandTask) {
  detailBusy.value = true
  error.value = ''
  try {
    detail.value = await apiRequest<CommandTaskDetail>(`/api/v1/commands/${task.id}`)
    detailOpen.value = true
  } catch (requestError) {
    error.value = requestError instanceof Error ? requestError.message : '无法读取任务详情'
  } finally {
    detailBusy.value = false
  }
}

async function retryCommand(task: CommandTask) {
  retryingTaskID.value = task.id
  error.value = ''
  try {
    await apiRequest<CommandTaskDetail>(`/api/v1/commands/${task.id}/retry`, {
      method: 'POST', body: '{}',
    })
    await loadTasks()
  } catch (requestError) {
    error.value = requestError instanceof Error ? requestError.message : '任务重试失败'
  } finally {
    retryingTaskID.value = ''
  }
}

function canRetry(task: CommandTask): boolean {
  return task.counts.failed + task.counts.timed_out > 0
}

function statusLabel(status: string): string {
  return ({
    queued: '排队中', leased: '已领取', running: '执行中', succeeded: '成功', failed: '失败', timed_out: '超时',
  } as Record<string, string>)[status] ?? status
}

function formatTime(value: string | null | undefined): string {
  return value ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : '-'
}

function formatDuration(value: number | null): string {
  if (value === null) return '-'
  if (value < 1000) return `${value} ms`
  return `${(value / 1000).toFixed(1)} s`
}

function refreshWhenVisible() {
  if (document.visibilityState === 'visible') void loadTasks()
}

onMounted(() => {
  void loadTasks()
  pollTimer = window.setInterval(refreshWhenVisible, 5_000)
})

onBeforeUnmount(() => {
  if (pollTimer !== undefined) window.clearInterval(pollTimer)
})
</script>

<template>
  <section class="command-center" aria-label="命令中心">
    <form class="command-composer" @submit.prevent="submitCommand">
      <div class="command-section-heading">
        <div>
          <p class="section-index">COMMAND / DISPATCH</p>
          <h2>下发远程命令</h2>
        </div>
        <span>{{ selectedNodeIDs.length }} / {{ windowsNodes.length }} 台已选择</span>
      </div>

      <fieldset class="command-device-fieldset">
        <legend>目标设备</legend>
        <p v-if="windowsNodes.length === 0" class="empty-state">暂无 Windows 设备</p>
        <div v-else class="command-device-list">
          <label v-for="node in windowsNodes" :key="node.id" :data-node-id="node.id">
            <input v-model="selectedNodeIDs" type="checkbox" :value="node.id">
            <span><strong>{{ node.name }}</strong><small>{{ node.remark || node.ip_address || '未填写备注' }}</small></span>
            <em :class="{ online: isNodeOnline(node.last_seen_at) }">{{ isNodeOnline(node.last_seen_at) ? '在线' : '离线' }}</em>
          </label>
        </div>
      </fieldset>

      <div class="command-options-row">
        <div>
          <label>Shell</label>
          <div class="segmented-control" aria-label="命令 Shell">
            <button type="button" :aria-pressed="shell === 'powershell'" @click="shell = 'powershell'">PowerShell</button>
            <button type="button" :aria-pressed="shell === 'cmd'" @click="shell = 'cmd'">CMD</button>
          </div>
        </div>
        <label for="command-timeout">超时（秒）
          <input id="command-timeout" v-model.number="timeoutSeconds" type="number" min="10" max="1800" step="10">
        </label>
      </div>

      <label class="command-editor-label" for="command-text">命令内容</label>
      <textarea id="command-text" v-model="command" spellcheck="false" maxlength="32768" placeholder="输入要执行的命令"></textarea>

      <div class="command-submit-row">
        <label class="risk-confirmation" for="command-risk">
          <input id="command-risk" v-model="riskConfirmed" type="checkbox">
          <span>确认命令将以 Windows LocalSystem 高权限执行，且命令中不包含明文密码或 Token。</span>
        </label>
        <button class="primary-button" data-action="submit-command" type="submit" :disabled="!canSubmit">
          <VideoPlay aria-hidden="true" />{{ submitBusy ? '下发中' : '下发命令' }}
        </button>
      </div>
      <p v-if="error" class="form-error" role="alert">{{ error }}</p>
    </form>

    <section class="command-history">
      <div class="command-section-heading">
        <div><p class="section-index">COMMAND / HISTORY</p><h2>任务历史</h2></div>
        <button class="icon-button" type="button" title="刷新命令任务" :disabled="loadingTasks" @click="loadTasks"><Refresh /></button>
      </div>
      <div class="command-table-scroll">
        <table>
          <thead><tr><th>创建时间</th><th>Shell</th><th>命令</th><th>目标</th><th>状态</th><th>操作</th></tr></thead>
          <tbody>
            <tr v-if="tasks.length === 0"><td colspan="6" class="empty-state">暂无命令任务</td></tr>
            <tr v-for="task in tasks" :key="task.id">
              <td>{{ formatTime(task.created_at) }}</td>
              <td><span class="shell-label">{{ task.shell === 'powershell' ? 'PowerShell' : 'CMD' }}</span></td>
              <td><code class="command-preview">{{ task.command }}</code></td>
              <td>{{ task.target_count }}</td>
              <td><div class="command-counts">
                <span v-if="task.counts.queued">排队 {{ task.counts.queued }}</span>
                <span v-if="task.counts.leased">领取 {{ task.counts.leased }}</span>
                <span v-if="task.counts.running">执行 {{ task.counts.running }}</span>
                <span v-if="task.counts.succeeded" class="success">成功 {{ task.counts.succeeded }}</span>
                <span v-if="task.counts.failed" class="danger">失败 {{ task.counts.failed }}</span>
                <span v-if="task.counts.timed_out" class="danger">超时 {{ task.counts.timed_out }}</span>
              </div></td>
              <td><div class="table-actions">
                <button class="icon-button" data-action="command-detail" type="button" title="查看任务详情" :disabled="detailBusy" @click="openDetail(task)"><Document /></button>
                <button v-if="canRetry(task)" class="icon-button" data-action="retry-command" type="button" title="重试失败设备" :disabled="retryingTaskID === task.id" @click="retryCommand(task)"><RefreshRight /></button>
              </div></td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <ElDrawer v-model="detailOpen" class="command-detail-drawer" title="命令任务详情" size="min(760px, 94vw)" destroy-on-close>
      <div v-if="detail" class="command-detail-list">
        <header><span>{{ detail.task.shell === 'powershell' ? 'PowerShell' : 'CMD' }}</span><code>{{ detail.task.command }}</code></header>
        <article v-for="execution in detail.executions" :key="execution.id">
          <div class="execution-heading">
            <strong>{{ execution.node_name }}</strong>
            <span :class="['execution-status', execution.status]">{{ statusLabel(execution.status) }}</span>
          </div>
          <dl>
            <div><dt>退出码</dt><dd>{{ execution.exit_code ?? '-' }}</dd></div>
            <div><dt>耗时</dt><dd>{{ formatDuration(execution.duration_ms) }}</dd></div>
            <div><dt>开始</dt><dd>{{ formatTime(execution.started_at) }}</dd></div>
            <div><dt>结束</dt><dd>{{ formatTime(execution.finished_at) }}</dd></div>
          </dl>
          <p v-if="execution.error_message" class="execution-error">{{ execution.error_message }}</p>
          <pre v-text="execution.output || '无输出'"></pre>
          <small v-if="execution.output_truncated">输出超过 256 KiB，已截断</small>
        </article>
      </div>
    </ElDrawer>
  </section>
</template>
