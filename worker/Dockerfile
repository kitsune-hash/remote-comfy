# Remote-Comfy Worker
# ComfyUI + runqy-worker + handler in one image
#
# Build: docker build -t remote-comfy-worker .
# Run:   docker run --gpus all -e RUNQY_URL=... -e RUNQY_API_KEY=... remote-comfy-worker

FROM pytorch/pytorch:2.6.0-cuda12.6-cudnn9-runtime

ENV DEBIAN_FRONTEND=noninteractive
ENV COMFY_URL=http://127.0.0.1:8188
ENV AZURE_CONTAINER=ephemeral

WORKDIR /workspace

# System deps
RUN apt-get update && apt-get install -y --no-install-recommends \
    git curl wget \
    libgl1 libglib2.0-0 \
    && rm -rf /var/lib/apt/lists/*

# Install ComfyUI
RUN git clone --depth 1 https://github.com/comfyanonymous/ComfyUI.git /workspace/ComfyUI \
    && cd /workspace/ComfyUI \
    && pip install --no-cache-dir -r requirements.txt

# Handler dependencies
COPY requirements.txt /workspace/handler/requirements.txt
RUN pip install --no-cache-dir -r /workspace/handler/requirements.txt

# Install runqy-worker binary
RUN curl -sL https://github.com/Publikey/runqy-worker/releases/download/v0.2.5/runqy-worker_0.2.5_linux_amd64.tar.gz \
    | tar -xz -C /usr/local/bin/ runqy-worker \
    && chmod +x /usr/local/bin/runqy-worker

# Copy handler code
COPY handler.py /workspace/handler/handler.py

# Worker config
COPY config.yml /workspace/handler/config.yml

# Startup script
COPY entrypoint.sh /workspace/entrypoint.sh
RUN chmod +x /workspace/entrypoint.sh

EXPOSE 8188

ENTRYPOINT ["/workspace/entrypoint.sh"]
