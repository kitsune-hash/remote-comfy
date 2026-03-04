#!/usr/bin/env python3
"""
Remote-Comfy Handler for runqy-worker (stdio protocol).

Receives jobs via stdin JSON lines, executes workflows on local ComfyUI,
relays progress to the gateway via WebSocket, returns results via stdout.
"""

import os
import sys
import json
import time
import uuid
import websocket
import requests

# Force unbuffered output for stdio protocol
sys.stdout = open(sys.stdout.fileno(), mode='w', buffering=1)
sys.stderr = open(sys.stderr.fileno(), mode='w', buffering=1)

# Config from environment
COMFY_URL = os.environ.get("COMFY_URL", "http://127.0.0.1:8188")

# Azure Blob Storage config
AZURE_STORAGE_CONNECTION_STRING = os.environ.get("AZURE_STORAGE_CONNECTION_STRING", "")
AZURE_CONTAINER = os.environ.get("AZURE_CONTAINER", "ephemeral")
AZURE_CDN_URL = os.environ.get("AZURE_CDN_URL", "https://storagesdxldev.blob.core.windows.net")


def log(msg):
    """Log to stderr (stdout is reserved for protocol)."""
    print(msg, file=sys.stderr, flush=True)


def respond(task_id, result=None, error=None, retry=False):
    """Send response to runqy-worker via stdout."""
    resp = {"task_id": task_id}
    if error:
        resp["error"] = error
        if retry:
            resp["retry"] = True
    elif result:
        resp["result"] = result
    print(json.dumps(resp), flush=True)


def upload_to_azure(image_data, filename, job_id):
    """Upload image to Azure Blob Storage and return public URL."""
    if not AZURE_STORAGE_CONNECTION_STRING:
        return None
    try:
        from azure.storage.blob import BlobServiceClient, ContentSettings
        blob_service = BlobServiceClient.from_connection_string(AZURE_STORAGE_CONNECTION_STRING)
        container_client = blob_service.get_container_client(AZURE_CONTAINER)
        blob_name = f"remote-comfy/{job_id}/{filename}"
        blob_client = container_client.get_blob_client(blob_name)
        blob_client.upload_blob(
            image_data, overwrite=True,
            content_settings=ContentSettings(content_type="image/png")
        )
        url = f"{AZURE_CDN_URL}/{AZURE_CONTAINER}/{blob_name}"
        log(f"[handler] Uploaded to Azure: {url}")
        return url
    except Exception as e:
        log(f"[handler] Azure upload failed: {e}")
        return None


def download_comfy_image(filename, subfolder=""):
    """Download image from ComfyUI."""
    params = {"filename": filename}
    if subfolder:
        params["subfolder"] = subfolder
    response = requests.get(f"{COMFY_URL}/view", params=params, timeout=30)
    response.raise_for_status()
    return response.content


def execute_workflow(task_id, payload):
    """Execute a ComfyUI workflow and relay progress to gateway."""
    job_id = payload.get("job_id")
    workflow = payload.get("workflow")
    gateway_url = payload.get("gateway_url", "")

    log(f"[handler] Executing job {job_id}")

    gateway_ws = None
    comfy_ws = None
    outputs = []
    error = None
    start_time = time.time()

    try:
        # Connect to gateway WebSocket for progress relay
        if gateway_url:
            ws_url = gateway_url.replace("http://", "ws://").replace("https://", "wss://")
            ws_url = f"{ws_url}/api/worker/connect/{job_id}"
            log(f"[handler] Connecting to gateway: {ws_url}")
            gateway_ws = websocket.create_connection(ws_url, timeout=30)
            log(f"[handler] Connected to gateway")

        # Connect to ComfyUI WebSocket for progress
        client_id = str(uuid.uuid4())
        comfy_ws_url = COMFY_URL.replace("http://", "ws://") + f"/ws?clientId={client_id}"
        comfy_ws = websocket.create_connection(comfy_ws_url, timeout=10)
        log(f"[handler] Connected to ComfyUI (client_id: {client_id})")

        # Submit workflow to ComfyUI
        prompt_response = requests.post(
            f"{COMFY_URL}/prompt",
            json={"prompt": workflow, "client_id": client_id},
            timeout=30
        )
        prompt_response.raise_for_status()
        prompt_id = prompt_response.json().get("prompt_id")
        log(f"[handler] Submitted workflow, prompt_id: {prompt_id}")

        # Relay progress messages
        while True:
            try:
                comfy_ws.settimeout(120)
                msg = comfy_ws.recv()

                # Forward binary messages (preview images) as-is
                if isinstance(msg, bytes):
                    if gateway_ws:
                        gateway_ws.send(msg, opcode=websocket.ABNF.OPCODE_BINARY)
                    continue

                data = json.loads(msg)
                msg_type = data.get("type")

                if msg_type in ("executing", "executed", "execution_error", "execution_cached"):
                    log(f"[handler] ComfyUI: {msg_type} - {data.get('data', {})}")

                # Forward to gateway
                if gateway_ws:
                    gateway_ws.send(msg)

                if msg_type == "executing":
                    node = data.get("data", {}).get("node")
                    if node is None:
                        log(f"[handler] Execution complete")
                        break
                elif msg_type == "executed":
                    output_data = data.get("data", {}).get("output", {})
                    images = output_data.get("images", [])
                    for img in images:
                        filename = img.get("filename")
                        subfolder = img.get("subfolder", "")
                        if filename:
                            outputs.append({"filename": filename, "subfolder": subfolder})
                            # Upload to Azure immediately
                            try:
                                image_data = download_comfy_image(filename, subfolder)
                                azure_url = upload_to_azure(image_data, filename, job_id)
                                if azure_url and gateway_ws:
                                    gateway_ws.send(json.dumps({
                                        "type": "image_uploaded",
                                        "filename": filename,
                                        "subfolder": subfolder,
                                        "azure_url": azure_url
                                    }))
                            except Exception as e:
                                log(f"[handler] Early upload failed for {filename}: {e}")
                elif msg_type == "execution_error":
                    error = data.get("data", {}).get("exception_message", "Unknown error")
                    log(f"[handler] Execution error: {error}")
                    break

            except websocket.WebSocketTimeoutException:
                error = "ComfyUI execution timeout"
                break
            except Exception as e:
                error = str(e)
                break

        # Collect output URLs
        duration_ms = int((time.time() - start_time) * 1000)
        output_urls = []
        for img_info in outputs:
            filename = img_info["filename"]
            if AZURE_STORAGE_CONNECTION_STRING:
                output_urls.append(f"{AZURE_CDN_URL}/{AZURE_CONTAINER}/remote-comfy/{job_id}/{filename}")
            else:
                output_urls.append(f"{COMFY_URL}/view?filename={filename}")

        # Send completion/error to gateway
        if gateway_ws:
            if error:
                gateway_ws.send(json.dumps({"type": "error", "error": error}))
            else:
                gateway_ws.send(json.dumps({
                    "type": "completed",
                    "output_urls": output_urls,
                    "duration_ms": duration_ms
                }))
                log(f"[handler] Sent completion: {len(output_urls)} outputs, {duration_ms}ms")
            time.sleep(0.5)

    except Exception as e:
        error = str(e)
        log(f"[handler] Job failed: {e}")
        if gateway_ws:
            try:
                gateway_ws.send(json.dumps({"type": "error", "error": error}))
            except:
                pass
    finally:
        if comfy_ws:
            comfy_ws.close()
        if gateway_ws:
            gateway_ws.close()

    return error, {"output_urls": output_urls, "duration_ms": duration_ms} if not error else None


def main():
    log(f"[handler] Starting remote-comfy handler")
    log(f"[handler] ComfyUI: {COMFY_URL}")
    log(f"[handler] Azure: {AZURE_CONTAINER}@{AZURE_CDN_URL.split('/')[-1] if AZURE_CDN_URL else 'disabled'}")

    # Wait for ComfyUI to be ready
    for i in range(30):
        try:
            r = requests.get(f"{COMFY_URL}/system_stats", timeout=5)
            if r.status_code == 200:
                log(f"[handler] ComfyUI ready")
                break
        except:
            pass
        if i == 29:
            print(json.dumps({"status": "error", "error": "ComfyUI not available"}), flush=True)
            sys.exit(1)
        time.sleep(2)

    # Signal ready to runqy-worker
    print(json.dumps({"status": "ready"}), flush=True)
    log(f"[handler] Ready, waiting for tasks...")

    # Read tasks from stdin (JSON lines)
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue

        try:
            task = json.loads(line)
        except json.JSONDecodeError as e:
            log(f"[handler] Invalid JSON: {e}")
            continue

        task_id = task.get("task_id", "")
        payload = task.get("payload", {})

        # payload might be a string (double-encoded)
        if isinstance(payload, str):
            try:
                payload = json.loads(payload)
            except:
                pass

        log(f"[handler] Received task {task_id}: job={payload.get('job_id', '?')}")

        error, result = execute_workflow(task_id, payload)

        if error:
            respond(task_id, error=error)
        else:
            respond(task_id, result=result)


if __name__ == "__main__":
    main()
