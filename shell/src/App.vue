<template>
  <div class="flex h-full flex-col bg-zinc-950 text-white">
    <!-- Header -->
    <header class="flex items-center justify-between border-b border-zinc-800 px-4 py-2">
      <div class="flex items-center gap-3">
        <span class="text-lg font-semibold">⚡ Remote Comfy</span>
        <span class="rounded bg-zinc-800 px-2 py-0.5 text-xs text-zinc-400">beta</span>
      </div>

      <div class="flex items-center gap-4">
        <!-- Workers status -->
        <div class="flex items-center gap-2 text-sm">
          <span :class="statusDot">●</span>
          <span v-if="status">
            {{ status.workers.active }} GPU{{ status.workers.active !== 1 ? 's' : '' }} online
          </span>
          <span v-else class="text-zinc-500">connecting...</span>
        </div>

        <!-- Queue -->
        <div v-if="status && status.jobs.running > 0" class="flex items-center gap-1 text-sm text-amber-400">
          <span class="animate-pulse">◉</span>
          {{ status.jobs.running }} running
        </div>

        <!-- Jobs counter -->
        <div v-if="status" class="text-xs text-zinc-500">
          {{ status.jobs.completed }} generated
        </div>
      </div>
    </header>

    <!-- ComfyUI iframe -->
    <div class="flex-1 relative">
      <iframe
        ref="comfyFrame"
        :src="comfyUrl"
        class="absolute inset-0 h-full w-full border-0"
        allow="clipboard-read; clipboard-write"
      />
    </div>

    <!-- Status bar -->
    <footer class="flex items-center justify-between border-t border-zinc-800 px-4 py-1 text-xs text-zinc-500">
      <div class="flex items-center gap-4">
        <span v-if="status">
          Workers: {{ workerList }}
        </span>
      </div>
      <div class="flex items-center gap-4">
        <span v-if="status">Queue: {{ status.jobs.pending }} pending</span>
        <span>v0.1.0</span>
      </div>
    </footer>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'

const GATEWAY_URL = import.meta.env.VITE_GATEWAY_URL || 'https://ubuntu.tailfcee90.ts.net/gateway'
const comfyUrl = import.meta.env.VITE_COMFY_URL || GATEWAY_URL

const status = ref(null)
const comfyFrame = ref(null)
let pollInterval = null

const statusDot = computed(() => {
  if (!status.value) return 'text-zinc-600'
  if (status.value.workers.active > 0) return 'text-emerald-400'
  return 'text-red-400'
})

const workerList = computed(() => {
  if (!status.value?.workers?.list) return ''
  return status.value.workers.list
    .map(w => `${w.id.split(':')[0].slice(0, 8)} (${w.uptime})`)
    .join(', ')
})

async function fetchStatus() {
  try {
    const res = await fetch(`${GATEWAY_URL}/api/status`)
    if (res.ok) status.value = await res.json()
  } catch (e) {
    // silent
  }
}

onMounted(() => {
  fetchStatus()
  pollInterval = setInterval(fetchStatus, 10000)
})

onUnmounted(() => {
  if (pollInterval) clearInterval(pollInterval)
})
</script>
