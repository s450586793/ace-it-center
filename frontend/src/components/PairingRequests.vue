<script setup lang="ts">
import { computed, ref } from 'vue'
import { Plus } from '@element-plus/icons-vue'
import { ElMessageBox } from 'element-plus'
import { APIError, apiRequest } from '../api'
import type { NodeGroup, PairingRequest } from '../types'

const props = defineProps<{
  pairings: PairingRequest[]
  groups: NodeGroup[]
}>()

const emit = defineEmits<{ approved: []; rejected: []; 'create-group': [] }>()
const selectedGroups = ref<Record<string, string>>({})
const remarks = ref<Record<string, string>>({})
const actionBusy = ref<string | null>(null)
const actionError = ref('')
const pendingPairings = computed(() => props.pairings.filter(pairing => pairing.state === 'pending'))

function selectedGroup(pairing: PairingRequest): string {
  const selected = selectedGroups.value[pairing.id]
  if (selected !== undefined) return selected
  const recoveryGroup = pairing.existing_node?.group_id
  return recoveryGroup && props.groups.some(group => group.id === recoveryGroup) ? recoveryGroup : ''
}

function updateSelectedGroup(pairingID: string, event: Event) {
  selectedGroups.value[pairingID] = (event.target as HTMLSelectElement).value
}

function remark(pairingID: string): string {
  return remarks.value[pairingID] ?? ''
}

function updateRemark(pairingID: string, event: Event) {
  remarks.value[pairingID] = (event.target as HTMLTextAreaElement).value
}

function requestErrorMessage(requestError: unknown): string {
  if (requestError instanceof APIError && requestError.message) return requestError.message
  return '操作失败，请稍后重试'
}

async function approve(pairing: PairingRequest) {
  const groupID = selectedGroup(pairing)
  actionError.value = ''
  if (!groupID) {
    actionError.value = '请先创建并选择设备分组'
    return
  }

  actionBusy.value = pairing.id
  try {
    await apiRequest(`/api/v1/pairings/${pairing.id}/approve`, {
      method: 'POST', body: JSON.stringify({ group_id: groupID, remark: remark(pairing.id) }),
    })
    emit('approved')
  } catch (requestError) {
    actionError.value = requestErrorMessage(requestError)
  } finally {
    actionBusy.value = null
  }
}

async function reject(pairing: PairingRequest) {
  actionError.value = ''
  try {
    await ElMessageBox.confirm('拒绝后该设备需要重新发起配对请求。', '确认拒绝配对', {
      confirmButtonText: '拒绝配对', cancelButtonText: '取消', type: 'warning',
    })
  } catch {
    return
  }

  actionBusy.value = pairing.id
  try {
    await apiRequest(`/api/v1/pairings/${pairing.id}/reject`, { method: 'POST' })
    emit('rejected')
  } catch (requestError) {
    actionError.value = requestErrorMessage(requestError)
  } finally {
    actionBusy.value = null
  }
}
</script>

<template>
  <section class="pairing-requests" aria-labelledby="pairing-requests-title">
    <header class="section-toolbar">
      <div>
        <p class="section-index">待处理请求</p>
        <h2 id="pairing-requests-title">待配对设备</h2>
      </div>
      <div class="pairing-toolbar-actions">
        <small>{{ pendingPairings.length }} 个待处理请求</small>
        <button class="text-button" data-action="create-group" type="button" @click="emit('create-group')"><Plus />新建分组</button>
      </div>
    </header>

    <p v-if="actionError" class="form-error" role="alert">{{ actionError }}</p>
    <div v-if="pendingPairings.length" class="pairing-request-list">
      <article v-for="pairing in pendingPairings" :key="pairing.id" class="pairing-request">
        <div class="pairing-request-summary">
          <div>
            <strong>{{ pairing.hostname }}</strong>
            <small>{{ pairing.type }} · Agent {{ pairing.agent_version }}</small>
          </div>
          <span v-if="pairing.existing_node" class="pairing-recovery">恢复接入</span>
        </div>
        <dl class="pairing-request-meta">
          <div><dt>机器标识</dt><dd class="mono">{{ pairing.machine_id }}</dd></div>
          <div><dt>申请时间</dt><dd>{{ pairing.created_at }}</dd></div>
          <div><dt>到期时间</dt><dd>{{ pairing.expires_at }}</dd></div>
        </dl>
        <label class="pairing-group-field">
          <span>设备分组</span>
          <select :name="`group-${pairing.id}`" :value="selectedGroup(pairing)" :disabled="actionBusy === pairing.id" @change="updateSelectedGroup(pairing.id, $event)">
            <option value="">请选择设备分组</option>
            <option v-for="group in groups" :key="group.id" :value="group.id">{{ group.name }}</option>
          </select>
        </label>
        <label class="pairing-group-field">
          <span>备注</span>
          <textarea :name="`remark-${pairing.id}`" :value="remark(pairing.id)" :disabled="actionBusy === pairing.id" maxlength="500" placeholder="例如：15 楼财务电脑" @input="updateRemark(pairing.id, $event)"></textarea>
        </label>
        <div class="pairing-actions">
          <button class="secondary-button" :data-action="`reject-${pairing.id}`" type="button" :disabled="actionBusy === pairing.id" @click="reject(pairing)">拒绝</button>
          <button class="primary-button" :data-action="`approve-${pairing.id}`" type="button" :disabled="actionBusy === pairing.id" @click="approve(pairing)">{{ actionBusy === pairing.id ? '处理中' : '批准接入' }}</button>
        </div>
      </article>
    </div>
    <div v-else class="empty-state"><strong>暂无待配对设备</strong><span>新的设备配对请求会显示在这里。</span></div>
  </section>
</template>
