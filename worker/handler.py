#!/usr/bin/env python3
"""
remote-comfy handler — Called by runqy-worker for each job.
Executes ComfyUI workflow and relays progress to gateway.

Input: JSON payload on stdin (from runqy-worker)
Output: JSON result on stdout (to runqy-worker)

Environment (injected by runqy-worker from vault):
    AZURE_STORAGE_CONNECTION_STRING
    GATEWAY_URL
"""

import json
import os
import subprocess
import sys
import time
import uuid
from pathlib import Path

import requests
import websocket
from azure.storage.blob import BlobServiceClient, generate_blob_sas, BlobSasPermissions
from datetime import datetime, timedelta, timezone

# Config
COMFY_URL = os.getenv("COMFY_URL", "http://127.0.0.1:8188")
COMFY_DIR = os.getenv("COMFY_DIR", "/comfy")
AZURE_CONN_STR = os.getenv("AZURE_STORAGE_CONNECTION_STRING", "")
AZURE_CONTAINER = os.getenv("AZURE_CONTAINER", "outputs")

# Global ComfyUI process
comfy_process = None


def log(msg):
    print(f"[handler] {msg}", file=sys.stderr)


def start_comfy():
    """Start ComfyUI headless server if not running."""
    global comfy_process
    
    # Check if already running
    try:
        resp = requests.get(f"{COMFY_URL}/system_stats", timeout=2)
        if resp.status_code == 200:
            log("ComfyUI already running")
            return True
    except:
        pass
    
    log(f"Starting ComfyUI from {COMFY_DIR}...")
    comfy_process = subprocess.Popen(
        [sys.executable, "main.py", "--listen", "127.0.0.1", "--port", "8188", "--preview-method", "auto"],
        cwd=COMFY_DIR,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
    )
    
    # Wait for ready (2 min timeout)
    for i in range(120):
        try:
            resp = requests.get(f"{COMFY_URL}/system_stats", timeout=2)
            if resp.status_code == 200:
                log("ComfyUI is ready!")
                return True
        except:
            pass
        time.sleep(1)
    
    raise RuntimeError("ComfyUI failed to start within 2 minutes")


def upload_to_azure(file_path: str, job_id: str) -> str:
    """Upload file to Azure Blob, return pre-signed URL."""
    if not AZURE_CONN_STR:
        log("No Azure connection string, returning local path")
        return f"file://{file_path}"
    
    blob_service = BlobServiceClient.from_connection_string(AZURE_CONN_STR)
    container_client = blob_service.get_container_client(AZURE_CONTAINER)
    
    try:
        container_client.create_container()
    except:
        pass
    
    blob_name = f"{job_id}/{Path(file_path).name}"
    blob_client = container_client.get_blob_client(blob_name)
    
    with open(file_path, "rb") as f:
        blob_client.upload_blob(f, overwrite=True)
    
    sas_token = generate_blob_sas(
        account_name=blob_service.account_name,
        container_name=AZURE_CONTAINER,
        blob_name=blob_name,
        account_key=blob_service.credential.account_key,
        permission=BlobSasPermissions(read=True),
        expiry=datetime.now(timezone.utc) + timedelta(hours=24),
    )
    
    return f"{blob_client.url}?{sas_token}"


def execute_workflow(job_id: str, workflow: dict, gateway_url: str) -> dict:
    """Execute workflow, relay progress, return results."""
    start_time = time.time()
    output_urls = []
    
    # Connect to gateway WebSocket for progress relay
    gateway_ws_url = gateway_url.replace("http://", "ws://").replace("https://", "wss://")
    gateway_ws_url = f"{gateway_ws_url}/api/worker/connect/{job_id}"
    
    gw_ws = None
    try:
        gw_ws = websocket.create_connection(gateway_ws_url, timeout=10)
        log(f"Connected to gateway WS")
    except Exception as e:
        log(f"Failed to connect to gateway WS: {e}")
        # Continue anyway, results will still be uploaded
    
    try:
        # Submit workflow to ComfyUI
        client_id = str(uuid.uuid4())
        prompt_data = {"prompt": workflow, "client_id": client_id}
        
        resp = requests.post(f"{COMFY_URL}/prompt", json=prompt_data, timeout=10)
        if resp.status_code != 200:
            raise RuntimeError(f"ComfyUI rejected workflow: {resp.text}")
        
        prompt_id = resp.json().get("prompt_id")
        log(f"Workflow submitted, prompt_id: {prompt_id}")
        
        # Connect to ComfyUI WebSocket for progress
        comfy_ws = websocket.create_connection(
            f"{COMFY_URL.replace('http', 'ws')}/ws?clientId={client_id}",
            timeout=5,
        )
        
        # Relay progress events
        completed = False
        while not completed:
            try:
                result = comfy_ws.recv()
                if isinstance(result, str):
                    msg = json.loads(result)
                    msg_type = msg.get("type", "")
                    
                    # Relay to gateway
                    if gw_ws:
                        try:
                            gw_ws.send(result)
                        except:
                            pass
                    
                    # Check for completion
                    if msg_type == "executing" and msg.get("data", {}).get("node") is None:
                        completed = True
            except websocket.WebSocketTimeoutException:
                continue
            except Exception as e:
                log(f"ComfyUI WS error: {e}")
                break
        
        comfy_ws.close()
        
        # Fetch outputs from ComfyUI history
        try:
            hist_resp = requests.get(f"{COMFY_URL}/history/{prompt_id}", timeout=10)
            if hist_resp.status_code == 200:
                history = hist_resp.json()
                outputs = history.get(prompt_id, {}).get("outputs", {})
                
                for node_id, node_output in outputs.items():
                    if "images" in node_output:
                        for img in node_output["images"]:
                            filename = img["filename"]
                            subfolder = img.get("subfolder", "")
                            img_type = img.get("type", "output")
                            
                            # Download from ComfyUI
                            img_resp = requests.get(
                                f"{COMFY_URL}/view",
                                params={"filename": filename, "subfolder": subfolder, "type": img_type},
                                timeout=30,
                            )
                            if img_resp.status_code == 200:
                                local_path = f"/tmp/{job_id}_{filename}"
                                with open(local_path, "wb") as f:
                                    f.write(img_resp.content)
                                
                                url = upload_to_azure(local_path, job_id)
                                output_urls.append(url)
                                os.remove(local_path)
        except Exception as e:
            log(f"Failed to fetch outputs: {e}")
        
        duration_ms = int((time.time() - start_time) * 1000)
        
        # Send completion to gateway
        if gw_ws:
            try:
                gw_ws.send(json.dumps({
                    "type": "completed",
                    "output_urls": output_urls,
                    "duration_ms": duration_ms,
                }))
            except:
                pass
        
        log(f"Completed in {duration_ms}ms, {len(output_urls)} outputs")
        
        return {
            "success": True,
            "output_urls": output_urls,
            "duration_ms": duration_ms,
        }
        
    except Exception as e:
        log(f"Failed: {e}")
        if gw_ws:
            try:
                gw_ws.send(json.dumps({"type": "error", "error": str(e)}))
            except:
                pass
        return {
            "success": False,
            "error": str(e),
        }
    finally:
        if gw_ws:
            gw_ws.close()


def main():
    # Read job payload from stdin (passed by runqy-worker)
    input_data = sys.stdin.read()
    
    try:
        payload = json.loads(input_data)
    except json.JSONDecodeError as e:
        print(json.dumps({"success": False, "error": f"Invalid JSON input: {e}"}))
        sys.exit(1)
    
    job_id = payload.get("job_id", str(uuid.uuid4()))
    workflow = payload.get("workflow")
    gateway_url = payload.get("gateway_url", os.getenv("GATEWAY_URL", "http://localhost:8080"))
    
    if not workflow:
        print(json.dumps({"success": False, "error": "No workflow provided"}))
        sys.exit(1)
    
    log(f"Processing job {job_id}")
    
    # Ensure ComfyUI is running
    try:
        start_comfy()
    except Exception as e:
        print(json.dumps({"success": False, "error": f"Failed to start ComfyUI: {e}"}))
        sys.exit(1)
    
    # Execute workflow
    result = execute_workflow(job_id, workflow, gateway_url)
    
    # Output result to stdout (for runqy-worker)
    print(json.dumps(result))
    
    sys.exit(0 if result.get("success") else 1)


if __name__ == "__main__":
    main()
