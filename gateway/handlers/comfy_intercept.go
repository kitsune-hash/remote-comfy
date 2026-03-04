package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/kitsune-hash/remote-comfy/gateway/db"
	"github.com/kitsune-hash/remote-comfy/gateway/models"
)

// ComfyInterceptHandler intercepts ComfyUI /prompt and /ws endpoints
// to route execution through Runqy instead of direct proxy.
type ComfyInterceptHandler struct {
	store *db.Store

	mu          sync.RWMutex
	clientConns map[string]*websocket.Conn // clientId → frontend WS
	jobToClient     map[string]string          // jobId → clientId
	imageURLs       map[string]string          // filename → azure URL
}

func NewComfyInterceptHandler(store *db.Store) *ComfyInterceptHandler {
	return &ComfyInterceptHandler{
		store:       store,
		clientConns: make(map[string]*websocket.Conn),
		jobToClient: make(map[string]string),
		imageURLs:   make(map[string]string),
	}
}

// PromptRequest is the ComfyUI POST /prompt body
type PromptRequest struct {
	Prompt   interface{} `json:"prompt"`
	ClientID string      `json:"client_id"`
}

// HandlePrompt intercepts POST /prompt and POST /api/prompt
func (h *ComfyInterceptHandler) HandlePrompt(c *gin.Context) {
	var req PromptRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid prompt: " + err.Error()})
		return
	}

	jobID := uuid.New().String()

	// Store job
	if err := h.store.CreateJob(jobID, req.Prompt); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create job"})
		return
	}

	// Map job → client
	h.mu.Lock()
	if req.ClientID != "" {
		h.jobToClient[jobID] = req.ClientID
	}
	h.mu.Unlock()

	// Enqueue to Runqy
	if err := enqueueToRunqy(jobID, req.Prompt); err != nil {
		h.store.FailJob(jobID, "failed to enqueue: "+err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to enqueue"})
		return
	}

	log.Printf("[intercept] Job %s enqueued for client %s", jobID, req.ClientID)

	// Notify frontend WS about queue status
	h.sendToClient(req.ClientID, map[string]interface{}{
		"type": "status",
		"data": map[string]interface{}{
			"status": map[string]interface{}{
				"exec_info": map[string]interface{}{
					"queue_remaining": 1,
				},
			},
		},
	})

	// Return ComfyUI-compatible response
	c.JSON(http.StatusOK, gin.H{
		"prompt_id":   jobID,
		"number":      1,
		"node_errors": map[string]interface{}{},
	})
}

// HandleWS manages frontend WebSocket connections (/ws?clientId=xxx)
func (h *ComfyInterceptHandler) HandleWS(c *gin.Context) {
	clientID := c.Query("clientId")
	if clientID == "" {
		clientID = uuid.New().String()
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("[intercept-ws] Upgrade error for client %s: %v", clientID, err)
		return
	}

	log.Printf("[intercept-ws] Frontend connected: client=%s", clientID)

	// Store connection
	h.mu.Lock()
	if old, ok := h.clientConns[clientID]; ok {
		old.Close()
	}
	h.clientConns[clientID] = conn
	h.mu.Unlock()

	// Send initial status (ComfyUI sends this on connect)
	initMsg, _ := json.Marshal(map[string]interface{}{
		"type": "status",
		"data": map[string]interface{}{
			"status": map[string]interface{}{
				"exec_info": map[string]interface{}{
					"queue_remaining": 0,
				},
			},
			"sid": clientID,
		},
	})
	conn.WriteMessage(websocket.TextMessage, initMsg)

	// Keep alive
	defer func() {
		h.mu.Lock()
		if h.clientConns[clientID] == conn {
			delete(h.clientConns, clientID)
		}
		h.mu.Unlock()
		conn.Close()
		log.Printf("[intercept-ws] Frontend disconnected: client=%s", clientID)
	}()

	for {
		conn.SetReadDeadline(time.Now().Add(120 * time.Second))
		_, _, err := conn.ReadMessage()
		if err != nil {
			return
		}
	}
}

// HandleWorkerConnect is called when a worker connects via /api/worker/connect/:id
// It relays progress to the frontend WS for the matching clientId.
func (h *ComfyInterceptHandler) HandleWorkerConnect(c *gin.Context) {
	jobID := c.Param("id")

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("[intercept-worker] WS upgrade error for job %s: %v", jobID, err)
		return
	}

	log.Printf("[intercept-worker] Worker connected for job %s", jobID)
	h.store.UpdateJobStatus(jobID, models.StatusRunning)

	h.mu.RLock()
	clientID := h.jobToClient[jobID]
	h.mu.RUnlock()

	if clientID != "" {
		h.sendToClient(clientID, map[string]interface{}{
			"type": "execution_start",
			"data": map[string]interface{}{
				"prompt_id": jobID,
			},
		})
	}

	var localPromptID string

	defer func() {
		conn.Close()
		h.mu.Lock()
		delete(h.jobToClient, jobID)
		h.mu.Unlock()
	}()

	for {
		msgType, message, err := conn.ReadMessage()
		if err != nil {
			log.Printf("[intercept-worker] WS read error for job %s: %v", jobID, err)
			return
		}

		// Binary messages (preview images) — forward as-is
		if msgType == websocket.BinaryMessage {
			if clientID != "" {
				h.mu.RLock()
				frontendConn := h.clientConns[clientID]
				h.mu.RUnlock()
				if frontendConn != nil {
					frontendConn.WriteMessage(websocket.BinaryMessage, message)
				}
			}
			continue
		}

		// Text messages — parse, rewrite prompt_id, forward
		var msg map[string]interface{}
		if err := json.Unmarshal(message, &msg); err != nil {
			h.forwardToClient(clientID, message)
			continue
		}

		msgTypeStr, _ := msg["type"].(string)

		switch msgTypeStr {
		case "completed":
			var urls []string
			if rawURLs, ok := msg["output_urls"].([]interface{}); ok {
				for _, u := range rawURLs {
					if s, ok := u.(string); ok {
						urls = append(urls, s)
					}
				}
			}
			var dur int64
			if d, ok := msg["duration_ms"].(float64); ok {
				dur = int64(d)
			}
			h.store.CompleteJob(jobID, urls, dur)
			log.Printf("[intercept-worker] Job %s completed, %d outputs", jobID, len(urls))

			h.sendToClient(clientID, map[string]interface{}{
				"type": "status",
				"data": map[string]interface{}{
					"status": map[string]interface{}{
						"exec_info": map[string]interface{}{
							"queue_remaining": 0,
						},
					},
				},
			})
			return

		case "image_uploaded":
			if fn, ok := msg["filename"].(string); ok {
				if url, ok := msg["azure_url"].(string); ok {
					h.StoreImageURL(fn, url)
					log.Printf("[intercept-worker] Image mapped: %s → Azure", fn)
				}
			}
			continue
		case "error":
			errMsg := "unknown error"
			if e, ok := msg["error"].(string); ok {
				errMsg = e
			}
			h.store.FailJob(jobID, errMsg)
			log.Printf("[intercept-worker] Job %s failed: %s", jobID, errMsg)

			h.sendToClient(clientID, map[string]interface{}{
				"type": "execution_error",
				"data": map[string]interface{}{
					"prompt_id":         jobID,
					"exception_message": errMsg,
				},
			})
			return
		}

		// Rewrite ALL occurrences of local prompt_id in the JSON
		if data, ok := msg["data"].(map[string]interface{}); ok {
			if pid, ok := data["prompt_id"].(string); ok {
				if localPromptID == "" && pid != "" {
					localPromptID = pid
					log.Printf("[intercept-worker] Mapped local prompt %s → job %s", localPromptID, jobID)
				}
			}
		}

		var rewritten []byte
		if localPromptID != "" {
			// Deep replace all occurrences of local prompt_id with our job_id
			rewritten = []byte(strings.ReplaceAll(string(message), localPromptID, jobID))
		} else {
			// No local prompt_id yet, just rewrite data.prompt_id
			if data, ok := msg["data"].(map[string]interface{}); ok {
				data["prompt_id"] = jobID
				msg["data"] = data
			}
			if r, err := json.Marshal(msg); err == nil {
				rewritten = r
			} else {
				rewritten = message
			}
		}
		h.forwardToClient(clientID, rewritten)
	}
}

// HandleQueue intercepts POST /queue
func (h *ComfyInterceptHandler) HandleQueue(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"queue_running": []interface{}{},
		"queue_pending": []interface{}{},
	})
}

func (h *ComfyInterceptHandler) sendToClient(clientID string, msg interface{}) {
	if clientID == "" {
		return
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	h.forwardToClient(clientID, data)
}

func (h *ComfyInterceptHandler) forwardToClient(clientID string, data []byte) {
	if clientID == "" {
		return
	}
	h.mu.RLock()
	conn := h.clientConns[clientID]
	h.mu.RUnlock()
	if conn != nil {
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			log.Printf("[intercept] Send to client %s failed: %v", clientID, err)
		}
	}
}

// Image URL mapping: filename → azure URL
func (h *ComfyInterceptHandler) StoreImageURL(filename, url string) {
	h.mu.Lock()
	if h.imageURLs == nil {
		h.imageURLs = make(map[string]string)
	}
	h.imageURLs[filename] = url
	h.mu.Unlock()
}

func (h *ComfyInterceptHandler) GetImageURL(filename string) (string, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.imageURLs == nil {
		return "", false
	}
	url, ok := h.imageURLs[filename]
	return url, ok
}

// HandleView intercepts GET /api/view — redirects to Azure if available
func (h *ComfyInterceptHandler) HandleView(c *gin.Context) {
	filename := c.Query("filename")
	if filename != "" {
		if azureURL, ok := h.GetImageURL(filename); ok {
			log.Printf("[intercept-view] Redirecting %s → Azure", filename)
			c.Redirect(http.StatusTemporaryRedirect, azureURL)
			return
		}
	}
	// Not found in Azure — will fall through to proxy via next handler
	c.Set("fallthrough", true)
}

// HandleStatus returns gateway status including worker health from Runqy server.
func (h *ComfyInterceptHandler) HandleStatus(c *gin.Context) {
	runqyURL := os.Getenv("RUNQY_URL")
	if runqyURL == "" {
		runqyURL = "http://localhost:3000"
	}
	runqyAPIKey := os.Getenv("RUNQY_API_KEY")

	// Fetch workers from Runqy
	type RunqyWorker struct {
		WorkerID    string `json:"worker_id"`
		StartedAt   int64  `json:"started_at"`
		LastBeat    int64  `json:"last_beat"`
		Concurrency int    `json:"concurrency"`
		Status      string `json:"status"`
		IsStale     bool   `json:"is_stale"`
	}
	type RunqyWorkersResp struct {
		Workers []RunqyWorker `json:"workers"`
		Count   int           `json:"count"`
	}

	var workersResp RunqyWorkersResp
	workersOK := false

	req, err := http.NewRequest("GET", runqyURL+"/workers", nil)
	if err == nil {
		if runqyAPIKey != "" {
			req.Header.Set("Authorization", "Bearer "+runqyAPIKey)
		}
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(req)
		if err == nil {
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if json.Unmarshal(body, &workersResp) == nil {
				workersOK = true
			}
		}
	}

	// Count active workers
	activeWorkers := 0
	totalCapacity := 0
	var workerList []gin.H
	if workersOK {
		for _, w := range workersResp.Workers {
			if w.Status == "running" && !w.IsStale {
				activeWorkers++
				totalCapacity += w.Concurrency
				lastBeatAgo := time.Since(time.Unix(w.LastBeat, 0)).Round(time.Second)
				workerList = append(workerList, gin.H{
					"id":            w.WorkerID,
					"status":        w.Status,
					"concurrency":   w.Concurrency,
					"last_heartbeat": fmt.Sprintf("%s ago", lastBeatAgo),
					"uptime":        fmt.Sprintf("%s", time.Since(time.Unix(w.StartedAt, 0)).Round(time.Second)),
				})
			}
		}
	}

	// Count jobs from DB
	h.mu.RLock()
	activeJobs := len(h.jobToClient)
	connectedClients := len(h.clientConns)
	cachedImages := len(h.imageURLs)
	h.mu.RUnlock()

	// Get job stats from DB
	var pendingJobs, runningJobs, completedJobs, failedJobs int
	if stats, err := h.store.GetJobStats(); err == nil {
		pendingJobs = stats.Pending
		runningJobs = stats.Running
		completedJobs = stats.Completed
		failedJobs = stats.Failed
	}

	c.JSON(200, gin.H{
		"status":  "ok",
		"service": "remote-comfy-gateway",
		"version": "0.3.0",
		"workers": gin.H{
			"active":   activeWorkers,
			"capacity": totalCapacity,
			"list":     workerList,
		},
		"jobs": gin.H{
			"active_relays": activeJobs,
			"pending":       pendingJobs,
			"running":       runningJobs,
			"completed":     completedJobs,
			"failed":        failedJobs,
		},
		"connections": gin.H{
			"websocket_clients": connectedClients,
			"cached_images":     cachedImages,
		},
		"runqy_server": workersOK,
	})
}

// Cached ComfyUI data (populated by worker on connect)
type ComfyCache struct {
	mu   sync.RWMutex
	data map[string]json.RawMessage // path → response body
}

var comfyCache = &ComfyCache{
	data: make(map[string]json.RawMessage),
}

func (c *ComfyCache) Set(key string, value json.RawMessage) {
	c.mu.Lock()
	c.data[key] = value
	c.mu.Unlock()
	log.Printf("[cache] Cached %s (%d bytes)", key, len(value))
}

func (c *ComfyCache) Get(key string) (json.RawMessage, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.data[key]
	return v, ok
}

// HandleCachedEndpoint serves cached ComfyUI responses or returns 503 if not yet available.
func (h *ComfyInterceptHandler) HandleCachedEndpoint(c *gin.Context) {
	path := c.Request.URL.Path
	// Normalize: /api/object_info and /object_info both map to /object_info
	path = strings.TrimPrefix(path, "/api")

	if data, ok := comfyCache.Get(path); ok {
		c.Header("Content-Type", "application/json")
		c.Writer.Write(data)
		return
	}
	c.JSON(503, gin.H{"error": "worker not connected yet, data not available"})
}

// HandleWorkerCacheUpdate receives cached endpoint data from worker via POST /api/worker/cache
func (h *ComfyInterceptHandler) HandleWorkerCacheUpdate(c *gin.Context) {
	var payload struct {
		Endpoint string          `json:"endpoint"`
		Data     json.RawMessage `json:"data"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	comfyCache.Set(payload.Endpoint, payload.Data)
	c.JSON(200, gin.H{"ok": true})
}
