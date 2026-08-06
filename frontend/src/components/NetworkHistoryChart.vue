<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { LineChart } from 'echarts/charts'
import { GridComponent, LegendComponent, TooltipComponent } from 'echarts/components'
import { init, use, type ECharts } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import type { NetworkHistoryPoint } from '../types'
import { buildNetworkChartOption } from '../lib/network-chart'

use([LineChart, GridComponent, LegendComponent, TooltipComponent, CanvasRenderer])

const props = defineProps<{ points: NetworkHistoryPoint[] }>()
const container = ref<HTMLElement | null>(null)
let chart: ECharts | undefined
let resizeObserver: ResizeObserver | undefined
let themeObserver: MutationObserver | undefined

function themeOption() {
  const styles = getComputedStyle(document.documentElement)
  const ink = styles.getPropertyValue('--ink').trim()
  const muted = styles.getPropertyValue('--muted').trim()
  const line = styles.getPropertyValue('--line').trim()
  const surface = styles.getPropertyValue('--surface').trim()
  return {
    color: [styles.getPropertyValue('--accent').trim(), styles.getPropertyValue('--warning').trim()],
    backgroundColor: 'transparent',
    textStyle: { color: ink },
    legend: { textStyle: { color: ink } },
    tooltip: { backgroundColor: surface, borderColor: line, textStyle: { color: ink } },
    xAxis: { axisLine: { lineStyle: { color: line } }, axisLabel: { color: muted } },
    yAxis: {
      nameTextStyle: { color: muted },
      axisLabel: { color: muted },
      splitLine: { lineStyle: { color: line } },
    },
  }
}

function renderChart() {
  if (!chart) return
  const base = buildNetworkChartOption(props.points)
  const theme = themeOption()
  const baseXAxis = Array.isArray(base.xAxis) ? base.xAxis[0] : base.xAxis
  chart.setOption({
    ...base,
    ...theme,
    legend: { ...base.legend, ...theme.legend },
    tooltip: { ...base.tooltip, ...theme.tooltip },
    xAxis: {
      ...baseXAxis,
      ...theme.xAxis,
      axisLabel: { ...baseXAxis?.axisLabel, ...theme.xAxis.axisLabel },
    },
    yAxis: { ...base.yAxis, ...theme.yAxis },
  }, true)
}

onMounted(async () => {
  await nextTick()
  if (!container.value) return
  chart = init(container.value)
  renderChart()

  resizeObserver = new ResizeObserver(() => chart?.resize())
  resizeObserver.observe(container.value)
  themeObserver = new MutationObserver(renderChart)
  themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
})

watch(() => props.points, renderChart, { deep: true })

onBeforeUnmount(() => {
  resizeObserver?.disconnect()
  themeObserver?.disconnect()
  chart?.dispose()
  chart = undefined
})
</script>

<template>
  <div
    ref="container"
    class="network-history-chart"
    role="img"
    aria-label="设备下载和上传平均速率历史曲线"
  ></div>
</template>
