#!/bin/bash
set -e

echo "[entrypoint] Starting ComfyUI..."
cd /workspace/ComfyUI
python3 main.py --listen 0.0.0.0 --port 8188 --disable-auto-launch &
COMFY_PID=$!

# Wait for ComfyUI to be ready
echo "[entrypoint] Waiting for ComfyUI..."
for i in $(seq 1 60); do
    if curl -s http://127.0.0.1:8188/system_stats > /dev/null 2>&1; then
        echo "[entrypoint] ComfyUI ready"
        break
    fi
    if [ $i -eq 60 ]; then
        echo "[entrypoint] ComfyUI failed to start"
        exit 1
    fi
    sleep 2
done

# Start runqy-worker (handler.py is started by it via stdio protocol)
echo "[entrypoint] Starting runqy-worker..."
cd /workspace/handler
exec runqy-worker -config config.yml
