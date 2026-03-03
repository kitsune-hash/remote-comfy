# remote-comfy 🌩️

Cloud ComfyUI execution platform. Design workflows in your browser, run them on remote GPUs.

## Architecture

```
┌──────────────┐     ┌──────────────┐     ┌──────────┐     ┌──────────────┐
│   Frontend   │────▶│   Gateway    │────▶│  Runqy   │────▶│   Worker     │
│   (Vue 3)    │◀────│   (Go/Gin)   │◀────│  Queue   │◀────│  (ComfyUI)   │
│              │ WS  │              │     │          │     │  Vast.ai GPU │
└──────────────┘     └──────────────┘     └──────────┘     └──────────────┘
                            │                                      │
                            └──────────── Azure Blob ◀─────────────┘
                                          (outputs)
```

## Components

| Component | Tech | Location |
|-----------|------|----------|
| Frontend | Vue 3 (fork ComfyUI_frontend) | `frontend/` |
| Gateway | Go + Gin + SQLite | `gateway/` |
| Worker | Python + ComfyUI + Docker | `worker/` |

## Quick Start (Development)

### 1. Start Gateway

```bash
cd gateway
export RUNQY_URL=http://localhost:3000
export RUNQY_API_KEY=your-key
export GATEWAY_URL=http://localhost:8080
go run .
```

### 2. Start Worker (needs GPU)

```bash
cd worker
pip install -r requirements.txt
export RUNQY_URL=http://localhost:3000
export GATEWAY_URL=http://localhost:8080
export COMFY_DIR=/path/to/ComfyUI
python worker.py
```

### 3. Start Frontend

```bash
# Clone ComfyUI frontend
git clone https://github.com/Comfy-Org/ComfyUI_frontend.git
cd ComfyUI_frontend
pnpm install
DEV_SERVER_COMFYUI_URL='http://localhost:8080/' pnpm dev
```

## Docker Compose (Gateway + Redis)

```bash
docker-compose up -d
```

## API

### Execute Workflow
```bash
curl -X POST http://localhost:8080/api/workflow/execute \
  -H 'Content-Type: application/json' \
  -d '{"workflow": {...}}'
```

### Check Status
```bash
curl http://localhost:8080/api/workflow/status/{job_id}
```

### Stream Progress (WebSocket)
```javascript
const ws = new WebSocket('ws://localhost:8080/api/workflow/stream/{job_id}');
ws.onmessage = (e) => console.log(JSON.parse(e.data));
```

## MVP Scope

- ✅ Gateway with WebSocket relay
- ✅ Worker with ComfyUI headless execution
- ✅ SQLite job tracking
- ✅ Azure Blob output upload
- ⬜ Auth (v2)
- ⬜ Billing (v2)
- ⬜ Model catalogue (v2)

## Stack

- **Frontend:** Vue 3 → Azure Static Web Apps
- **Gateway:** Go + Gin → VPS
- **DB:** SQLite (MVP) → Postgres (v2)
- **Queue:** Runqy + Redis
- **Workers:** Docker + PyTorch → Vast.ai
- **Storage:** Azure Blob CDN

## License

Proprietary — All rights reserved.
