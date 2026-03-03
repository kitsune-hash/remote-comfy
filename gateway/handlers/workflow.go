package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/kitsune-hash/remote-comfy/gateway/db"
	"github.com/kitsune-hash/remote-comfy/gateway/models"
	"github.com/kitsune-hash/remote-comfy/gateway/relay"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for MVP
	},
}

type WorkflowHandler struct {
	store    *db.Store
	relayMgr *relay.Manager
}

func NewWorkflowHandler(store *db.Store, relayMgr *relay.Manager) *WorkflowHandler {
	return &WorkflowHandler{store: store, relayMgr: relayMgr}
}

// Execute handles POST /api/workflow/execute
func (h *WorkflowHandler) Execute(c *gin.Context) {
	var req models.ExecuteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid workflow: " + err.Error()})
		return
	}

	jobID := uuid.New().String()

	if err := h.store.CreateJob(jobID, req.Workflow); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create job"})
		return
	}

	if err := enqueueToRunqy(jobID, req.Workflow); err != nil {
		h.store.FailJob(jobID, "failed to enqueue: "+err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to enqueue job"})
		return
	}

	log.Printf("[execute] Job %s created and enqueued", jobID)

	c.JSON(http.StatusOK, models.ExecuteResponse{
		JobID:  jobID,
		Status: string(models.StatusPending),
		Stream: fmt.Sprintf("/api/workflow/stream/%s", jobID),
	})
}

// Status handles GET /api/workflow/status/:id
func (h *WorkflowHandler) Status(c *gin.Context) {
	jobID := c.Param("id")
	job, err := h.store.GetJob(jobID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "job not found"})
		return
	}
	c.JSON(http.StatusOK, models.StatusResponse{Job: *job})
}

// Result handles GET /api/workflow/result/:id
func (h *WorkflowHandler) Result(c *gin.Context) {
	jobID := c.Param("id")
	job, err := h.store.GetJob(jobID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "job not found"})
		return
	}
	c.JSON(http.StatusOK, models.ResultResponse{
		JobID:      job.ID,
		Status:     string(job.Status),
		OutputURLs: job.OutputURLs,
		Error:      job.Error,
	})
}

// Stream handles WS /api/workflow/stream/:id (frontend connects here)
func (h *WorkflowHandler) Stream(c *gin.Context) {
	jobID := c.Param("id")

	if _, err := h.store.GetJob(jobID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "job not found"})
		return
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("[stream] WS upgrade error for job %s: %v", jobID, err)
		return
	}
	defer conn.Close()

	log.Printf("[stream] Frontend connected for job %s", jobID)

	session := h.relayMgr.GetOrCreate(jobID)
	session.SetFrontend(conn)

	// Send initial status
	job, _ := h.store.GetJob(jobID)
	if job != nil {
		statusMsg, _ := json.Marshal(map[string]string{
			"type":   "status",
			"status": string(job.Status),
		})
		conn.WriteMessage(websocket.TextMessage, statusMsg)
	}

	// Keep alive, listen for cancel
	for {
		select {
		case <-session.Done():
			return
		default:
		}

		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Printf("[stream] Frontend disconnected for job %s", jobID)
			return
		}

		var msg map[string]string
		if json.Unmarshal(message, &msg) == nil && msg["type"] == "cancel" {
			log.Printf("[stream] Cancel requested for job %s", jobID)
		}
	}
}

// WorkerConnect handles WS /api/worker/connect/:id (worker connects here)
func (h *WorkflowHandler) WorkerConnect(c *gin.Context) {
	jobID := c.Param("id")

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("[worker] WS upgrade error for job %s: %v", jobID, err)
		return
	}

	log.Printf("[worker] Worker connected for job %s", jobID)
	h.store.UpdateJobStatus(jobID, models.StatusRunning)

	session := h.relayMgr.GetOrCreate(jobID)
	session.SetWorker(conn)

	// Notify frontend
	if session.FrontendWS != nil {
		msg, _ := json.Marshal(map[string]string{"type": "status", "status": "running"})
		session.FrontendWS.WriteMessage(websocket.TextMessage, msg)
	}

	// Relay worker → frontend
	session.RelayWorkerToFrontend(
		func(outputURLs []string, durationMs int64) {
			log.Printf("[worker] Job %s completed, %d outputs", jobID, len(outputURLs))
			h.store.CompleteJob(jobID, outputURLs, durationMs)
			h.relayMgr.Remove(jobID)
		},
		func(errMsg string) {
			log.Printf("[worker] Job %s failed: %s", jobID, errMsg)
			h.store.FailJob(jobID, errMsg)
			h.relayMgr.Remove(jobID)
		},
	)
}

// enqueueToRunqy sends a job to the Runqy queue via HTTP API
func enqueueToRunqy(jobID string, workflow interface{}) error {
	runqyURL := getEnv("RUNQY_URL", "http://localhost:3000")
	runqyQueue := getEnv("RUNQY_QUEUE", "comfy")
	runqyAPIKey := os.Getenv("RUNQY_API_KEY")
	gatewayURL := getEnv("GATEWAY_URL", "http://localhost:8080")

	payload := map[string]interface{}{
		"queue":   runqyQueue,
		"timeout": 1200,
		"data": map[string]interface{}{
			"job_id":      jobID,
			"workflow":    workflow,
			"gateway_url": gatewayURL,
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", runqyURL+"/queue/add", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if runqyAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+runqyAPIKey)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("runqy request failed: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("runqy returned status %d", resp.StatusCode)
	}

	return nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
