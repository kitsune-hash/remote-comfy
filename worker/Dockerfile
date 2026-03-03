FROM pytorch/pytorch:2.2.0-cuda12.1-cudnn8-runtime

ENV DEBIAN_FRONTEND=noninteractive

# System dependencies
RUN apt-get update && apt-get install -y \
    git \
    wget \
    curl \
    libgl1-mesa-glx \
    libglib2.0-0 \
    && rm -rf /var/lib/apt/lists/*

# Install runqy-worker binary
RUN curl -fsSL https://github.com/Publikey/runqy-worker/releases/latest/download/runqy-worker-linux-amd64 \
    -o /usr/local/bin/runqy-worker && \
    chmod +x /usr/local/bin/runqy-worker

# Clone ComfyUI
RUN git clone https://github.com/comfyanonymous/ComfyUI.git /comfy
WORKDIR /comfy
RUN pip install --no-cache-dir -r requirements.txt

# Install ComfyUI-Manager
RUN cd custom_nodes && \
    git clone https://github.com/ltdrdata/ComfyUI-Manager.git

# Install popular custom nodes
RUN cd custom_nodes && \
    git clone https://github.com/cubiq/ComfyUI_IPAdapter_plus && \
    git clone https://github.com/Kosinkadink/ComfyUI-VideoHelperSuite && \
    git clone https://github.com/Fannovel16/comfyui_controlnet_aux && \
    git clone https://github.com/ltdrdata/ComfyUI-Impact-Pack && \
    git clone https://github.com/pythongosssss/ComfyUI-Custom-Scripts && \
    git clone https://github.com/jags111/efficiency-nodes-comfyui && \
    git clone https://github.com/WASasquatch/was-node-suite-comfyui && \
    git clone https://github.com/Suzie1/ComfyUI_Comfyroll_CustomNodes && \
    git clone https://github.com/rgthree/rgthree-comfy && \
    git clone https://github.com/kijai/ComfyUI-KJNodes

# Install custom node dependencies
RUN for dir in custom_nodes/*/; do \
    if [ -f "$dir/requirements.txt" ]; then \
        pip install --no-cache-dir -r "$dir/requirements.txt" 2>/dev/null || true; \
    fi; \
    done

# Handler dependencies
COPY requirements.txt /worker/requirements.txt
RUN pip install --no-cache-dir -r /worker/requirements.txt

# Handler script (called by runqy-worker)
COPY handler.py /worker/handler.py
RUN chmod +x /worker/handler.py

# Model directory (mount or download at boot)
RUN mkdir -p /comfy/models/checkpoints /comfy/models/loras /comfy/models/vae

# Runqy worker config
COPY worker-config.toml /etc/runqy-worker/config.toml

ENV COMFY_DIR=/comfy
ENV COMFY_URL=http://127.0.0.1:8188
ENV PYTHONUNBUFFERED=1

# runqy-worker is the entrypoint, it handles job pulling and calls our handler
ENTRYPOINT ["runqy-worker"]
CMD ["--config", "/etc/runqy-worker/config.toml"]
