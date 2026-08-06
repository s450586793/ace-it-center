<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ElDrawer } from 'element-plus'
import 'element-plus/theme-chalk/el-drawer.css'
import { apiRequest } from '../api'
import type { NetworkHistoryResponse, NetworkRange, Node } from '../types'
import NetworkHistoryChart from './NetworkHistoryChart.vue'

const props = defineProps<{
  modelValue: boolean
  node: Node
}>()

const emit = defineEmits<{ 'update:modelValue': [value: boolean] }>()
const ranges: { value: NetworkRange; label: string }[] = [
  { value: '24h', label: '24 小时' },
  { value: '7d', label: '7 天' },
  { value: '30d', label: '30 天' },
  { value: '90d', label: '90 天' },
]
const range = ref<NetworkRange>('24h')
const history = ref<NetworkHistoryResponse | null>(null)
const loading = ref(false)
const loadError = ref('')
const requestSequence = ref(0)

const aggregates = computed(() => {
  const points = history.value?.points ?? []
  if (!points.length) return null
  const total = points.reduce((result, point) => ({
    upload: result.upload + point.upload_avg_mb_s,
    download: result.download + point.download_avg_mb_s,
    uploadPeak: Math.max(result.uploadPeak, point.upload_peak_mb_s),
    downloadPeak: Math.max(result.downloadPeak, point.download_peak_mb_s),
  }), { upload: 0, download: 0, uploadPeak: 0, downloadPeak: 0 })
  return {
    uploadAverage: total.upload / points.length,
    downloadAverage: total.download / points.length,
    uploadPeak: total.uploadPeak,
    downloadPeak: total.downloadPeak,
  }
})

function safeMessage(error: unknown): string {
  return error instanceof Error ? error.message : '无法加载网络历史'
}

function rate(value: number): string {
  return `${Number.isFinite(value) ? value.toFixed(2) : '0.00'} MB/s`
}

async function loadHistory() {
  const sequence = ++requestSequence.value
  loading.value = true
  loadError.value = ''
  try {
    const result = await apiRequest<NetworkHistoryResponse>(
      `/api/v1/nodes/${props.node.id}/network-history?range=${range.value}`,
    )
    if (sequence === requestSequence.value) history.value = result
  } catch (error) {
    if (sequence === requestSequence.value) {
      history.value = null
      loadError.value = safeMessage(error)
    }
  } finally {
    if (sequence === requestSequence.value) loading.value = false
  }
}

function selectRange(value: NetworkRange) {
  if (range.value === value) return
  range.value = value
  void loadHistory()
}

watch(() => props.modelValue, open => {
  if (open) void loadHistory()
  else requestSequence.value += 1
})

watch(() => props.node.id, () => {
  range.value = '24h'
  history.value = null
  if (props.modelValue) void loadHistory()
})

onMounted(() => {
  if (props.modelValue) void loadHistory()
})
onBeforeUnmount(() => { requestSequence.value += 1 })
</script>

<template>
  <ElDrawer
    class="network-history-drawer"
    :model-value="modelValue"
    size="min(560px, 100%)"
    :show-close="true"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <template #header>
      <div class="drawer-heading">
        <p class="section-index">NETWORK HISTORY</p>
        <h2>{{ node.name }}</h2>
        <span>{{ node.ip_address || '无 IP 地址' }}</span>
      </div>
    </template>

    <div class="range-segment" aria-label="历史范围">
      <button
        v-for="item in ranges"
        :key="item.value"
        type="button"
        :data-range="item.value"
        :aria-pressed="range === item.value"
        @click="selectRange(item.value)"
      >{{ item.label }}</button>
    </div>

    <div v-if="loading" class="network-state" role="status">正在加载网络历史…</div>
    <div v-else-if="loadError" class="network-state error" role="alert">
      <strong>历史数据加载失败</strong>
      <span>{{ loadError }}</span>
      <button class="secondary-button" data-action="retry" type="button" @click="loadHistory">重试</button>
    </div>
    <div v-else-if="!history?.points.length" class="network-state">
      <strong>暂无网络历史数据</strong>
      <span>此范围内尚未收到有效样本。</span>
    </div>
    <template v-else>
      <dl v-if="aggregates" class="history-metrics">
        <div><dt>平均下载</dt><dd>{{ rate(aggregates.downloadAverage) }}</dd></div>
        <div><dt>峰值下载</dt><dd>{{ rate(aggregates.downloadPeak) }}</dd></div>
        <div><dt>平均上传</dt><dd>{{ rate(aggregates.uploadAverage) }}</dd></div>
        <div><dt>峰值上传</dt><dd>{{ rate(aggregates.uploadPeak) }}</dd></div>
      </dl>
      <NetworkHistoryChart :points="history.points" />
    </template>
  </ElDrawer>
</template>
