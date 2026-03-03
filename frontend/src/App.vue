<template>
  <div class="container">
    <header>
      <h1>🎨 Remote ComfyUI</h1>
      <p class="subtitle">Cloud-powered image generation</p>
    </header>

    <main>
      <!-- Prompt Input -->
      <div class="card">
        <label for="prompt">Prompt</label>
        <textarea 
          id="prompt" 
          v-model="prompt" 
          placeholder="A beautiful sunset over mountains..."
          :disabled="isRunning"
        ></textarea>
        
        <label for="negative">Negative Prompt</label>
        <input 
          id="negative" 
          v-model="negativePrompt" 
          placeholder="ugly, blurry, low quality"
          :disabled="isRunning"
        />

        <div class="row">
          <div>
            <label for="seed">Seed</label>
            <input id="seed" type="number" v-model.number="seed" :disabled="isRunning" />
          </div>
          <div>
            <label for="steps">Steps</label>
            <input id="steps" type="number" v-model.number="steps" min="1" max="50" :disabled="isRunning" />
          </div>
        </div>

        <button @click="generate" :disabled="isRunning || !prompt">
          {{ isRunning ? 'Generating...' : '✨ Generate' }}
        </button>
      </div>

      <!-- Progress -->
      <div v-if="status" class="card status-card">
        <div class="status-header">
          <span class="status-badge" :class="status">{{ status }}</span>
          <span v-if="currentNode" class="node">Node: {{ currentNode }}</span>
        </div>
        <div v-if="progress.length" class="progress-log">
          <div v-for="(msg, i) in progress.slice(-5)" :key="i" class="log-line">
            {{ msg }}
          </div>
        </div>
      </div>

      <!-- Result -->
      <div v-if="resultUrl" class="card result-card">
        <h3>Result</h3>
        <img :src="resultUrl" alt="Generated image" />
        <div class="result-meta">
          <span>Duration: {{ duration }}ms</span>
          <a :href="resultUrl" target="_blank">Open full size ↗</a>
        </div>
      </div>

      <!-- Error -->
      <div v-if="error" class="card error-card">
        <h3>❌ Error</h3>
        <p>{{ error }}</p>
      </div>
    </main>

    <footer>
      <p>Powered by <a href="https://runqy.com" target="_blank">Runqy</a> • 
         <a href="https://github.com/comfyanonymous/ComfyUI" target="_blank">ComfyUI</a></p>
    </footer>
  </div>
</template>

<script setup>
import { ref } from 'vue'

const API_URL = import.meta.env.VITE_API_URL || ''
const WS_URL = import.meta.env.VITE_WS_URL || 'wss://ubuntu.tailfcee90.ts.net/gateway'

const prompt = ref('')
const negativePrompt = ref('ugly, blurry, low quality')
const seed = ref(Math.floor(Math.random() * 999999))
const steps = ref(10)

const isRunning = ref(false)
const status = ref('')
const currentNode = ref('')
const progress = ref([])
const resultUrl = ref('')
const duration = ref(0)
const error = ref('')

function buildWorkflow() {
  return {
    "3": {
      "class_type": "KSampler",
      "inputs": {
        "cfg": 7,
        "denoise": 1,
        "latent_image": ["5", 0],
        "model": ["4", 0],
        "negative": ["7", 0],
        "positive": ["6", 0],
        "sampler_name": "euler",
        "scheduler": "normal",
        "seed": seed.value,
        "steps": steps.value
      }
    },
    "4": {
      "class_type": "CheckpointLoaderSimple",
      "inputs": { "ckpt_name": "v1-5-pruned-emaonly.safetensors" }
    },
    "5": {
      "class_type": "EmptyLatentImage",
      "inputs": { "batch_size": 1, "height": 512, "width": 512 }
    },
    "6": {
      "class_type": "CLIPTextEncode",
      "inputs": { "clip": ["4", 1], "text": prompt.value }
    },
    "7": {
      "class_type": "CLIPTextEncode",
      "inputs": { "clip": ["4", 1], "text": negativePrompt.value }
    },
    "8": {
      "class_type": "VAEDecode",
      "inputs": { "samples": ["3", 0], "vae": ["4", 2] }
    },
    "9": {
      "class_type": "SaveImage",
      "inputs": { "filename_prefix": "remote_comfy", "images": ["8", 0] }
    }
  }
}

async function generate() {
  isRunning.value = true
  status.value = 'pending'
  currentNode.value = ''
  progress.value = []
  resultUrl.value = ''
  error.value = ''
  duration.value = 0

  try {
    // Submit workflow
    const res = await fetch(`${API_URL}/api/workflow/execute`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ workflow: buildWorkflow() })
    })
    
    if (!res.ok) throw new Error('Failed to submit workflow')
    
    const { job_id, stream_url } = await res.json()
    progress.value.push(`Job created: ${job_id}`)
    
    // Connect to WebSocket for progress
    const wsUrl = `${WS_URL}${stream_url}`
    const ws = new WebSocket(wsUrl)
    
    ws.onmessage = (event) => {
      const data = JSON.parse(event.data)
      
      if (data.type === 'status') {
        status.value = data.status
      } else if (data.type === 'executing') {
        if (data.data?.node) {
          currentNode.value = data.data.node
          progress.value.push(`Executing node ${data.data.node}`)
        }
      } else if (data.type === 'executed') {
        progress.value.push(`Node ${data.data?.node} completed`)
      } else if (data.type === 'completed') {
        status.value = 'completed'
        if (data.output_urls?.length) {
          resultUrl.value = data.output_urls[0]
        }
        duration.value = data.duration_ms || 0
        ws.close()
      } else if (data.type === 'error') {
        status.value = 'failed'
        error.value = data.error
        ws.close()
      }
    }
    
    ws.onerror = () => {
      error.value = 'WebSocket connection failed'
      status.value = 'failed'
    }
    
    ws.onclose = () => {
      isRunning.value = false
    }
    
  } catch (e) {
    error.value = e.message
    status.value = 'failed'
    isRunning.value = false
  }
}
</script>

<style scoped>
.container {
  max-width: 600px;
  margin: 0 auto;
  padding: 2rem 1rem;
}

header {
  text-align: center;
  margin-bottom: 2rem;
}

h1 {
  font-size: 2rem;
  margin-bottom: 0.5rem;
}

.subtitle {
  color: #888;
}

.card {
  background: #252542;
  border-radius: 12px;
  padding: 1.5rem;
  margin-bottom: 1rem;
}

label {
  display: block;
  margin-bottom: 0.5rem;
  color: #aaa;
  font-size: 0.9rem;
}

textarea, input {
  width: 100%;
  padding: 0.75rem;
  border: 1px solid #3a3a5c;
  border-radius: 8px;
  background: #1a1a2e;
  color: #eee;
  font-size: 1rem;
  margin-bottom: 1rem;
}

textarea {
  min-height: 80px;
  resize: vertical;
}

.row {
  display: flex;
  gap: 1rem;
}

.row > div {
  flex: 1;
}

button {
  width: 100%;
  padding: 1rem;
  border: none;
  border-radius: 8px;
  background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
  color: white;
  font-size: 1.1rem;
  font-weight: 600;
  cursor: pointer;
  transition: opacity 0.2s;
}

button:hover:not(:disabled) {
  opacity: 0.9;
}

button:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.status-card {
  background: #1e1e3f;
}

.status-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 0.5rem;
}

.status-badge {
  padding: 0.25rem 0.75rem;
  border-radius: 20px;
  font-size: 0.85rem;
  font-weight: 500;
}

.status-badge.pending { background: #f59e0b; color: #000; }
.status-badge.running { background: #3b82f6; }
.status-badge.completed { background: #10b981; }
.status-badge.failed, .status-badge.timeout { background: #ef4444; }

.node {
  color: #888;
  font-size: 0.85rem;
}

.progress-log {
  font-family: monospace;
  font-size: 0.8rem;
  color: #888;
  max-height: 100px;
  overflow-y: auto;
}

.log-line {
  padding: 0.25rem 0;
  border-bottom: 1px solid #2a2a4a;
}

.result-card img {
  width: 100%;
  border-radius: 8px;
  margin: 1rem 0;
}

.result-meta {
  display: flex;
  justify-content: space-between;
  color: #888;
  font-size: 0.9rem;
}

.result-meta a {
  color: #667eea;
}

.error-card {
  background: #3f1e1e;
  border: 1px solid #ef4444;
}

.error-card h3 {
  margin-bottom: 0.5rem;
}

footer {
  text-align: center;
  margin-top: 2rem;
  color: #666;
  font-size: 0.85rem;
}

footer a {
  color: #667eea;
  text-decoration: none;
}
</style>

// Build 1772555620
