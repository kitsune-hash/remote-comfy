#!/usr/bin/env python3
"""
Simple Remote-Comfy Worker
Polls Runqy queue via Redis, executes workflows on ComfyUI, relays to gateway.
"""

import os
import sys
import json
import time
import redis
import websocket
import requests
import threading
from urllib.parse import urljoin

# Force unbuffered output
sys.stdout = sys.stderr = open(sys.stdout.fileno(), mode='w', buffering=1)

# Config from environment
REDIS_URL = os.environ.get("REDIS_URL", "redis://localhost:6379")
QUEUE_NAME = os.environ.get("QUEUE_NAME", "comfy.default")
COMFY_URL = os.environ.get("COMFY_URL", "http://127.0.0.1:8188")
WORKER_ID = os.environ.get("WORKER_ID", f"worker-{os.getpid()}")

# Azure Blob Storage config
AZURE_STORAGE_CONNECTION_STRING = os.environ.get("AZURE_STORAGE_CONNECTION_STRING", "")
AZURE_CONTAINER = os.environ.get("AZURE_CONTAINER", "ephemeral")
AZURE_CDN_URL = os.environ.get("AZURE_CDN_URL", "https://storagesdxldev.blob.core.windows.net")

print(f"[worker] Starting simple worker {WORKER_ID}")
print(f"[worker] Redis: {REDIS_URL.split('@')[-1] if '@' in REDIS_URL else REDIS_URL}")
print(f"[worker] Queue: {QUEUE_NAME}")
print(f"[worker] ComfyUI: {COMFY_URL}")
print(f"[worker] Azure: {AZURE_CONTAINER}@{AZURE_CDN_URL.split('/')[-1] if AZURE_CDN_URL else 'disabled'}")


def upload_to_azure(image_data: bytes, filename: str, job_id: str) -> str:
    """Upload image to Azure Blob Storage and return public URL"""
    if not AZURE_STORAGE_CONNECTION_STRING:
        print(f"[worker] Azure not configured, skipping upload")
        return None
    
    try:
        from azure.storage.blob import BlobServiceClient, ContentSettings
        
        blob_service = BlobServiceClient.from_connection_string(AZURE_STORAGE_CONNECTION_STRING)
        container_client = blob_service.get_container_client(AZURE_CONTAINER)
        
        # Use job_id as folder for organization
        blob_name = f"remote-comfy/{job_id}/{filename}"
        
        blob_client = container_client.get_blob_client(blob_name)
        blob_client.upload_blob(
            image_data,
            overwrite=True,
            content_settings=ContentSettings(content_type="image/png")
        )
        
        # Return public URL
        url = f"{AZURE_CDN_URL}/{AZURE_CONTAINER}/{blob_name}"
        print(f"[worker] Uploaded to Azure: {url}")
        return url
        
    except Exception as e:
        print(f"[worker] Azure upload failed: {e}")
        return None


def download_comfy_image(filename: str, subfolder: str = "") -> bytes:
    """Download image from ComfyUI"""
    params = {"filename": filename}
    if subfolder:
        params["subfolder"] = subfolder
    
    response = requests.get(f"{COMFY_URL}/view", params=params, timeout=30)
    response.raise_for_status()
    return response.content


def get_redis_client():
    """Connect to Redis"""
    # Don't decode responses - asynq uses binary data
    return redis.from_url(REDIS_URL, decode_responses=False)


def fetch_job(r):
    """Fetch a job from the Runqy/asynq queue"""
    # Asynq uses asynq:{queue}:pending for pending jobs
    pending_key = f"asynq:{{{QUEUE_NAME}}}:pending"
    active_key = f"asynq:{{{QUEUE_NAME}}}:active"
    
    # Blocking pop with 5 second timeout
    result = r.brpoplpush(pending_key, active_key, timeout=5)
    if not result:
        return None
    
    # Result is bytes, decode it
    task_id = result.decode('utf-8') if isinstance(result, bytes) else result
    print(f"[worker] Got task: {task_id}")
    
    # Get the task data from hash
    task_key = f"asynq:{{{QUEUE_NAME}}}:t:{task_id}"
    # Don't use decode_responses for binary data
    task_data = r.hgetall(task_key)
    if not task_data:
        print(f"[worker] Task {task_id} not found in Redis")
        r.lrem(active_key, 1, result)
        return None
    
    # Parse job payload from protobuf 'msg' field
    try:
        msg = task_data.get(b"msg", b"")
        msg_str = msg.decode('latin-1')
        
        # Extract JSON from protobuf message
        json_start = msg_str.find('{')
        json_end = msg_str.rfind('}') + 1
        if json_start < 0 or json_end <= json_start:
            print(f"[worker] No JSON found in task message")
            r.lrem(active_key, 1, result)
            return None
        
        json_str = msg_str[json_start:json_end]
        payload = json.loads(json_str)
        
        return {
            "id": task_id,
            "job_id": payload.get("job_id"),
            "workflow": payload.get("workflow"),
            "gateway_url": payload.get("gateway_url"),
            "active_key": active_key,
            "task_key": task_key,
        }
    except (json.JSONDecodeError, UnicodeDecodeError) as e:
        print(f"[worker] Failed to parse task payload: {e}")
        r.lrem(active_key, 1, result)
        return None


def complete_job(r, job):
    """Mark job as completed in Redis (asynq format)"""
    r.lrem(job["active_key"], 1, job["id"])
    # Asynq handles completion, just remove from active
    r.lpush(f"asynq:{{{QUEUE_NAME}}}:completed", job["id"])
    print(f"[worker] Job {job['id']} completed")


def fail_job(r, job, error):
    """Mark job as failed in Redis (asynq format)"""
    r.lrem(job["active_key"], 1, job["id"])
    r.lpush(f"asynq:{{{QUEUE_NAME}}}:failed", job["id"])
    print(f"[worker] Job {job['id']} failed: {error}")


def execute_workflow(job):
    """Execute workflow on ComfyUI and relay to gateway"""
    job_id = job["job_id"]
    workflow = job["workflow"]
    gateway_url = job["gateway_url"]
    
    print(f"[worker] Executing job {job_id}")
    
    # Connect to gateway WebSocket
    ws_url = gateway_url.replace("http://", "ws://").replace("https://", "wss://")
    ws_url = urljoin(ws_url + "/", f"api/worker/connect/{job_id}")
    print(f"[worker] Connecting to gateway: {ws_url}")
    
    gateway_ws = None
    comfy_ws = None
    outputs = []
    error = None
    start_time = time.time()
    
    try:
        gateway_ws = websocket.create_connection(ws_url, timeout=30)
        print(f"[worker] Connected to gateway")
        
        # Generate client ID for ComfyUI WebSocket
        import uuid
        client_id = str(uuid.uuid4())
        
        # Connect to ComfyUI WebSocket for progress
        comfy_ws_url = COMFY_URL.replace("http://", "ws://") + f"/ws?clientId={client_id}"
        comfy_ws = websocket.create_connection(comfy_ws_url, timeout=10)
        print(f"[worker] Connected to ComfyUI (client_id: {client_id})")
        
        # Submit workflow to ComfyUI with client_id
        prompt_response = requests.post(
            f"{COMFY_URL}/prompt",
            json={"prompt": workflow, "client_id": client_id},
            timeout=30
        )
        prompt_response.raise_for_status()
        prompt_data = prompt_response.json()
        prompt_id = prompt_data.get("prompt_id")
        print(f"[worker] Submitted workflow, prompt_id: {prompt_id}")
        
        # Relay progress messages
        while True:
            try:
                comfy_ws.settimeout(120)
                msg = comfy_ws.recv()
                data = json.loads(msg)
                msg_type = data.get("type")
                
                # Debug: log message types
                if msg_type in ("executing", "executed", "execution_error", "execution_cached"):
                    print(f"[worker] ComfyUI msg: {msg_type} - {data.get('data', {})}")
                
                # Forward to gateway
                gateway_ws.send(msg)
                
                if msg_type == "executing":
                    node = data.get("data", {}).get("node")
                    if node is None:
                        print(f"[worker] Execution complete")
                        break
                elif msg_type == "executed":
                    output_data = data.get("data", {}).get("output", {})
                    images = output_data.get("images", [])
                    for img in images:
                        filename = img.get("filename")
                        subfolder = img.get("subfolder", "")
                        if filename:
                            # Store for later upload (after sending WS messages)
                            outputs.append({"filename": filename, "subfolder": subfolder})
                elif msg_type == "execution_error":
                    error = data.get("data", {}).get("exception_message", "Unknown error")
                    print(f"[worker] Execution error: {error}")
                    break
                    
            except websocket.WebSocketTimeoutException:
                print(f"[worker] ComfyUI timeout")
                error = "ComfyUI execution timeout"
                break
            except Exception as e:
                print(f"[worker] Error during execution: {e}")
                error = str(e)
                break
        
        # Upload outputs to Azure (do this after ComfyUI is done, but before sending completion)
        output_urls = []
        for img_info in outputs:
            filename = img_info["filename"]
            subfolder = img_info.get("subfolder", "")
            try:
                image_data = download_comfy_image(filename, subfolder)
                azure_url = upload_to_azure(image_data, filename, job_id)
                if azure_url:
                    output_urls.append(azure_url)
                else:
                    output_urls.append(f"{COMFY_URL}/view?filename={filename}&subfolder={subfolder}")
            except Exception as e:
                print(f"[worker] Failed to upload {filename}: {e}")
                output_urls.append(f"{COMFY_URL}/view?filename={filename}&subfolder={subfolder}")
        
        duration_ms = int((time.time() - start_time) * 1000)
        if error:
            gateway_ws.send(json.dumps({"type": "error", "error": error}))
        else:
            gateway_ws.send(json.dumps({
                "type": "completed",
                "output_urls": output_urls,
                "duration_ms": duration_ms
            }))
            print(f"[worker] Sent completion: {len(output_urls)} outputs, {duration_ms}ms")
        
        # Wait for gateway to process before closing
        time.sleep(0.5)
            
    except Exception as e:
        error = str(e)
        print(f"[worker] Job failed: {e}")
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
    
    return error


def main():
    r = get_redis_client()
    print(f"[worker] Connected to Redis, waiting for jobs...")
    
    while True:
        try:
            job = fetch_job(r)
            if job is None:
                continue
            
            print(f"[worker] Got job: {job['job_id']}")
            error = execute_workflow(job)
            
            if error:
                fail_job(r, job, error)
            else:
                complete_job(r, job)
                
        except KeyboardInterrupt:
            print(f"[worker] Shutting down...")
            break
        except Exception as e:
            print(f"[worker] Unexpected error: {e}")
            time.sleep(5)


if __name__ == "__main__":
    main()
