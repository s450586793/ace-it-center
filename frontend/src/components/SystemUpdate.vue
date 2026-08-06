<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { ElDialog } from 'element-plus'
import { APIError, apiRequest } from '../api'
import type { SystemUpdateStage, SystemUpdateStatus, SystemUpdateTask } from '../types'

const emit = defineEmits<{ 'session-expired': [] }>()

const stages: Array<{ value: SystemUpdateStage; label: string }> = [
  { value: 'checking', label: '正在检查升级条件' },
  { value: 'backing_up', label: '正在创建配置备份' },
  { value: 'pulling', label: '正在拉取升级包' },
  { value: 'switching_backend', label: '正在切换后端服务' },
  { value: 'checking_backend', label: '正在检查后端服务' },
  { value: 'switching_web', label: '正在切换 Web 服务' },
  { value: 'checking_web', label: '正在检查 Web 服务' },
  { value: 'stabilizing', label: '正在进行稳定性检查' },
  { value: 'cleaning', label: '正在完成升级清理' },
  { value: 'rolling_back', label: '正在回滚服务版本' },
]

const terminalStages: SystemUpdateStage[] = ['succeeded', 'failed', 'manual_intervention']
const status = ref<SystemUpdateStatus | null>(null)
const checkBusy = ref(false)
const startBusy = ref(false)
const confirmationOpen = ref(false)
const safeNotice = ref('')
let pollTimer: number | undefined
let retryDelay = 2_000
let mounted = false
let sessionExpired = false

const task = computed(() => status.value?.task)
const activeTask = computed(() => Boolean(task.value && !terminalStages.includes(task.value.stage)))
const blockedTask = computed(() => task.value?.stage === 'manual_intervention')
const canStart = computed(() => {
  const latest = status.value?.latest
  return Boolean(
    status.value?.update_available && latest && latest.backend === latest.web &&
    !activeTask.value && !blockedTask.value && !startBusy.value,
  )
})
const taskNotice = computed(() => {
  if (task.value?.stage === 'succeeded' && task.value.cleanup === 'pending') {
    return '升级已完成，请在 DSM Container Manager 中完成旧版本清理。'
  }
  if (task.value?.stage === 'failed') {
    return '升级未完成，系统已停止自动变更。请检查 DSM 服务状态后联系管理员处理。'
  }
  if (task.value?.stage === 'manual_intervention') {
    return '请在 DSM 中检查服务状态后联系管理员处理。'
  }
  return ''
})

function isUnauthorized(error: unknown): boolean {
  return error instanceof APIError && error.status === 401
}

function isRetryablePollingError(error: unknown): boolean {
  return error instanceof TypeError || (error instanceof APIError && (error.status === 502 || error.status === 503))
}

function clearPollTimer() {
  if (pollTimer !== undefined) {
    window.clearTimeout(pollTimer)
    pollTimer = undefined
  }
}

function schedulePoll(delay = retryDelay) {
  clearPollTimer()
  if (!mounted || sessionExpired || !activeTask.value) return
  pollTimer = window.setTimeout(() => { void refreshStatus(false) }, delay)
}

async function refreshStatus(check: boolean) {
  if (checkBusy.value) return
  checkBusy.value = true
  safeNotice.value = ''
  let shouldPoll = false
  try {
    status.value = check
      ? await apiRequest<SystemUpdateStatus>('/api/v1/system/update/check', { method: 'POST', body: '{}' })
      : await apiRequest<SystemUpdateStatus>('/api/v1/system/update')
    retryDelay = 2_000
    shouldPoll = true
  } catch (requestError) {
    if (isUnauthorized(requestError)) {
      sessionExpired = true
      clearPollTimer()
      emit('session-expired')
      return
    }
    if (activeTask.value && isRetryablePollingError(requestError)) {
      retryDelay = Math.min(retryDelay * 2, 5_000)
      shouldPoll = true
    } else {
      safeNotice.value = '暂时无法检查升级状态，请稍后重试。'
    }
  } finally {
    checkBusy.value = false
    if (shouldPoll) schedulePoll()
  }
}

function openConfirmation() {
  if (canStart.value) confirmationOpen.value = true
}

async function startUpdate() {
  const latest = status.value?.latest
  if (!canStart.value || !latest || startBusy.value) return

  startBusy.value = true
  safeNotice.value = ''
  try {
    const nextTask = await apiRequest<SystemUpdateTask>('/api/v1/system/update', {
      method: 'POST', body: JSON.stringify({ target_version: latest.backend }),
    })
    if (status.value) status.value = { ...status.value, task: nextTask }
    confirmationOpen.value = false
    retryDelay = 2_000
    schedulePoll()
  } catch (requestError) {
    if (isUnauthorized(requestError)) {
      sessionExpired = true
      clearPollTimer()
      emit('session-expired')
      return
    }
    safeNotice.value = '无法启动系统升级，请稍后重试。'
  } finally {
    startBusy.value = false
  }
}

function formatPublishedAt(value: string | undefined): string {
  if (!value || Number.isNaN(new Date(value).getTime())) return '-'
  return new Date(value).toLocaleString('zh-CN', { hour12: false })
}

function currentStageLabel(stage: SystemUpdateStage | undefined): string {
  return stages.find(item => item.value === stage)?.label
    ?? (stage === 'succeeded' ? '升级已完成' : stage === 'failed' ? '升级未完成' : stage === 'manual_intervention' ? '需要人工处理' : '等待升级检查')
}

function stageIsComplete(stage: SystemUpdateStage | undefined, item: SystemUpdateStage): boolean {
  if (!stage) return false
  return stages.findIndex(entry => entry.value === stage) > stages.findIndex(entry => entry.value === item)
}

onMounted(() => {
  mounted = true
  sessionExpired = false
  void refreshStatus(true)
})

onBeforeUnmount(() => {
  mounted = false
  clearPollTimer()
})
</script>

<template>
  <section class="system-update" aria-labelledby="system-update-title">
    <header class="system-update-heading">
      <div>
        <p class="section-index">SYSTEM / UPDATE</p>
        <h2 id="system-update-title">系统升级</h2>
      </div>
      <button
        class="icon-button"
        data-action="refresh-update"
        type="button"
        aria-label="检查系统升级"
        title="检查系统升级"
        :disabled="checkBusy || activeTask || blockedTask"
        @click="refreshStatus(true)"
      ><Refresh aria-hidden="true" /></button>
    </header>

    <div class="system-update-versions" aria-label="系统版本">
      <div class="system-update-version-row" data-version="current">
        <span>当前版本</span>
        <div class="system-update-version-value"><code>后端 {{ status?.current.backend ?? '-' }}</code><code>Web {{ status?.current.web ?? '-' }}</code></div>
      </div>
      <div class="system-update-version-row" data-version="latest">
        <span>最新稳定版</span>
        <div class="system-update-version-value"><code>后端 {{ status?.latest?.backend ?? '-' }}</code><code>Web {{ status?.latest?.web ?? '-' }}</code></div>
        <small>{{ formatPublishedAt(status?.latest?.published_at) }}</small>
      </div>
    </div>

    <div class="system-update-actions">
      <button class="primary-button system-update-start" data-action="start-update" type="button" :disabled="!canStart" @click="openConfirmation">
        {{ startBusy ? '正在提交' : '立即升级' }}
      </button>
      <p v-if="safeNotice" class="form-error" role="alert">{{ safeNotice }}</p>
      <p v-else-if="taskNotice" class="system-update-notice" role="status">{{ taskNotice }}</p>
    </div>

    <section class="system-update-progress" aria-label="升级进度">
      <div class="system-update-progress-heading">
        <span>升级状态</span>
        <strong data-stage="current">{{ currentStageLabel(task?.stage) }}</strong>
      </div>
      <ol class="system-update-status-list">
        <li v-for="item in stages" :key="item.value" :class="{ active: task?.stage === item.value, complete: stageIsComplete(task?.stage, item.value) }">
          <span aria-hidden="true"></span>{{ item.label }}
        </li>
      </ol>
    </section>

    <ElDialog v-model="confirmationOpen" title="确认系统升级" width="min(520px, calc(100vw - 32px))" align-center>
      <form class="system-update-confirmation" @submit.prevent="startUpdate">
        <p>确认后，系统将按已检查的稳定版本升级。</p>
        <dl>
          <div><dt>当前后端</dt><dd><code>{{ status?.current.backend }}</code></dd></div>
          <div><dt>目标后端</dt><dd><code>{{ status?.latest?.backend }}</code></dd></div>
          <div><dt>当前 Web</dt><dd><code>{{ status?.current.web }}</code></dd></div>
          <div><dt>目标 Web</dt><dd><code>{{ status?.latest?.web }}</code></dd></div>
        </dl>
        <div class="dialog-actions">
          <button class="secondary-button" type="button" :disabled="startBusy" @click="confirmationOpen = false">取消</button>
          <button class="primary-button" data-action="confirm-update" type="submit" :disabled="!canStart">{{ startBusy ? '正在提交' : '确认升级' }}</button>
        </div>
      </form>
    </ElDialog>
  </section>
</template>
