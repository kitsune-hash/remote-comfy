# remote-comfy Frontend

Fork of [Comfy-Org/ComfyUI_frontend](https://github.com/Comfy-Org/ComfyUI_frontend) (Vue 3 + TypeScript).

## Setup

1. Fork/clone the official ComfyUI frontend:
   ```bash
   git clone https://github.com/Comfy-Org/ComfyUI_frontend.git
   cd ComfyUI_frontend
   pnpm install
   ```

2. Configure API endpoint to point to our gateway instead of localhost:
   ```bash
   # Use the dev:cloud script pattern:
   DEV_SERVER_COMFYUI_URL='https://api.remote-comfy.com/' pnpm dev
   ```

3. For production build:
   ```bash
   pnpm build
   # Deploy dist/ to Azure Static Web Apps
   ```

## Key Modifications Needed (v2)

- Custom auth UI (login/register)
- Credit display
- Workflow upload button
- Model browser
- Custom progress overlay

## Deploy to Azure Static Web Apps

```bash
az staticwebapp create \
    --name remote-comfy-frontend \
    --resource-group remote-comfy \
    --source . \
    --branch main \
    --app-location "/" \
    --output-location "dist"
```
