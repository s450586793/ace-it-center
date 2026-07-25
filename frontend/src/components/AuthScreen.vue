<script setup lang="ts">
import { computed, reactive } from 'vue'
import { Lock, User } from '@element-plus/icons-vue'

const props = defineProps<{
  mode: 'setup' | 'login'
  busy: boolean
  error: string
}>()

const emit = defineEmits<{
  submit: [credentials: { username: string; password: string }]
}>()

const form = reactive({ username: 'jarvis', password: '' })
const title = computed(() => props.mode === 'setup' ? '初始化 Owner' : '登录控制台')
const action = computed(() => props.mode === 'setup' ? '创建 Owner' : '登录')

function submit() {
  emit('submit', { username: form.username.trim(), password: form.password })
}
</script>

<template>
  <main class="auth-shell">
    <section class="auth-panel" aria-labelledby="auth-title">
      <div class="brand-lockup">
        <div class="brand-mark" aria-hidden="true"><span>A</span></div>
        <div>
          <p class="brand-name">Ace IT Center</p>
          <p class="brand-subtitle">Infrastructure Operations</p>
        </div>
      </div>

      <div class="auth-heading">
        <p class="section-index">01 / ACCESS</p>
        <h1 id="auth-title">{{ title }}</h1>
      </div>

      <form @submit.prevent="submit">
        <label for="username">账号</label>
        <div class="field-wrap">
          <User class="field-icon" aria-hidden="true" />
          <input id="username" v-model="form.username" name="username" autocomplete="username" required>
        </div>

        <label for="password">密码</label>
        <div class="field-wrap">
          <Lock class="field-icon" aria-hidden="true" />
          <input
            id="password"
            v-model="form.password"
            name="password"
            type="password"
            :autocomplete="mode === 'setup' ? 'new-password' : 'current-password'"
            minlength="15"
            required
          >
        </div>

        <p v-if="error" class="form-error" role="alert">{{ error }}</p>
        <button class="primary-button" type="submit" :disabled="busy">
          {{ busy ? '处理中' : action }}
        </button>
      </form>
    </section>
    <aside class="auth-status" aria-label="平台状态">
      <span class="status-light"></span>
      <span>PRIVATE CONTROL PLANE</span>
    </aside>
  </main>
</template>
