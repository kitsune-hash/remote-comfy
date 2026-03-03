# remote-comfy — POC

## Project Overview

**remote-comfy** is a cloud ComfyUI execution platform. Users open a web frontend (fork of ComfyUI), design/upload workflows, and execute them on remote GPU workers via Runqy task queue.

## Architecture

```
Frontend (Vue 3, fork ComfyUI_frontend) 
    → API Gateway (Go + Gin) 
    → Runqy Queue 
    → Worker (Docker + ComfyUI headless, Vast.ai GPU)
    → Azure Blob (outputs)
```

## MVP Scope (what to build NOW)

### Gateway (Go + Gin) — `gateway/`

The API gateway is the core. It:
1. Receives workflow execution requests from the frontend
2. Enqueues jobs to Runqy
3. Relays WebSocket progress between worker and frontend
4. Stores job state in SQLite
5. Serves output URLs (Azure Blob pre-signed)

**Endpoints:**
```
POST /api/workflow/execute     → Accept workflow JSON, enqueue to Runqy, return job_id
GET  /api/workflow/status/:id  → Return job status (pending/running/completed/failed)
GET  /api/workflow/result/:id  → Return output URLs
WS   /api/workflow/stream/:id  → Real-time progress relay (frontend ↔ worker)
WS   /api/worker/connect/:id   → Worker connects here to relay progress
GET  /api/health               → Health check
```

**Job flow:**
1. Frontend POST /api/workflow/execute with workflow JSON
2. Gateway saves job to SQLite (status: pending), enqueues to Runqy
3. Frontend opens WS to /api/workflow/stream/:job_id
4. Worker picks job from Runqy, connects WS to /api/worker/connect/:job_id
5. Gateway links the two WebSocket connections, relays messages
6. Worker sends ComfyUI progress events through the relay
7. Worker uploads outputs to Azure Blob, sends completion with URLs
8. Gateway updates SQLite (status: completed, output_urls), notifies frontend

**SQLite schema:**
```sql
CREATE TABLE jobs (
    id TEXT PRIMARY KEY,
    workflow JSON NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    started_at DATETIME,
    completed_at DATETIME,
    output_urls JSON,
    error TEXT,
    worker_id TEXT,
    duration_ms INTEGER
);
```

**Config (env vars):**
- RUNQY_URL — Runqy server URL
- RUNQY_API_KEY — Runqy API key  
- RUNQY_QUEUE — Queue name (default: "comfy")
- AZURE_STORAGE_CONNECTION_STRING — For pre-signed URLs
- AZURE_CONTAINER — Container name for outputs
- PORT — Server port (default: 8080)
- DB_PATH — SQLite path (default: ./data/remote-comfy.db)

**Dependencies:**
- github.com/gin-gonic/gin
- github.com/gorilla/websocket
- github.com/mattn/go-sqlite3
- github.com/google/uuid

**Important:**
- Use gorilla/websocket for both frontend and worker WS connections
- Job IDs are UUIDs
- No auth for MVP (open access)
- No billing for MVP
- Timeout: 20 minutes per job (kill if exceeded)
- CORS: allow all origins for MVP

### Worker (Python) — `worker/`

The worker runs on GPU machines (Vast.ai). It:
1. Pulls jobs from Runqy
2. Starts ComfyUI headless server
3. Submits workflow to local ComfyUI
4. Connects to Gateway WS to relay progress
5. Uploads outputs to Azure Blob

For MVP, the worker is a Python script that:
- Uses the Runqy Python SDK to pull jobs
- Manages a local ComfyUI process
- Connects to ComfyUI local WebSocket for progress
- Connects to Gateway WebSocket to relay
- Uploads output files to Azure Blob

**worker/worker.py** — Main worker script
**worker/Dockerfile** — Docker image with ComfyUI + SDXL base model baked in
**worker/requirements.txt** — Python dependencies

### Frontend — `frontend/`

For MVP, just create a README.md explaining:
- Fork Comfy-Org/ComfyUI_frontend
- Change the API endpoint from localhost:8188 to our gateway URL
- Build and deploy to Azure Static Web Apps

## Code Style
- Go: standard gofmt, error handling with early returns
- Python: black formatting, type hints
- Keep it simple, MVP quality (working > perfect)
