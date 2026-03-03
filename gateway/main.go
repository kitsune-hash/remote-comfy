package main

import (
	"log"
	"os"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"

	"github.com/kitsune-hash/remote-comfy/gateway/db"
	"github.com/kitsune-hash/remote-comfy/gateway/handlers"
	"github.com/kitsune-hash/remote-comfy/gateway/models"
	"github.com/kitsune-hash/remote-comfy/gateway/relay"
)

func main() {
	port := getEnv("PORT", "8080")
	dbPath := getEnv("DB_PATH", "./data/remote-comfy.db")
	comfyURL := getEnv("COMFY_URL", "")
	jobTimeout := 20 * time.Minute

	os.MkdirAll("./data", 0755)

	store, err := db.New(dbPath)
	if err != nil {
		log.Fatalf("Failed to init database: %v", err)
	}
	defer store.Close()
	log.Println("Database initialized")

	relayMgr := relay.NewManager()
	wfHandler := handlers.NewWorkflowHandler(store, relayMgr)

	var proxyHandler *handlers.ProxyHandler
	if comfyURL != "" {
		proxyHandler = handlers.NewProxyHandler(comfyURL)
		log.Printf("ComfyUI proxy enabled: %s", comfyURL)
	}

	r := gin.Default()

	r.Use(cors.New(cors.Config{
		AllowAllOrigins:  true,
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "HEAD"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Accept", "Authorization"},
		AllowWebSockets:  true,
		MaxAge:           12 * time.Hour,
	}))

	// WebSocket proxy
	if proxyHandler != nil {
		r.GET("/ws", proxyHandler.ProxyWS)
	}

	// Our custom API routes
	api := r.Group("/api")
	{
		api.GET("/health", func(c *gin.Context) {
			c.JSON(200, gin.H{
				"status":  "ok",
				"service": "remote-comfy-gateway",
				"version": "0.1.0",
			})
		})

		wf := api.Group("/workflow")
		{
			wf.POST("/execute", wfHandler.Execute)
			wf.GET("/status/:id", wfHandler.Status)
			wf.GET("/result/:id", wfHandler.Result)
			wf.GET("/stream/:id", wfHandler.Stream)
		}

		api.GET("/worker/connect/:id", wfHandler.WorkerConnect)
	}

	// Catch-all: proxy everything else to ComfyUI
	if proxyHandler != nil {
		r.NoRoute(func(c *gin.Context) {
			path := c.Request.URL.Path
			if strings.HasPrefix(path, "/api/workflow") ||
				strings.HasPrefix(path, "/api/worker") ||
				path == "/api/health" {
				c.JSON(404, gin.H{"error": "not found"})
				return
			}
			log.Printf("[proxy] %s %s", c.Request.Method, path)
			proxyHandler.ProxyHTTP(c)
		})
		log.Println("ComfyUI catch-all proxy enabled")
	}

	// Background: timeout checker
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			ids, err := store.GetTimedOutJobs(jobTimeout)
			if err != nil {
				log.Printf("[timeout] Error checking timeouts: %v", err)
				continue
			}
			for _, id := range ids {
				log.Printf("[timeout] Job %s timed out", id)
				store.UpdateJobStatus(id, models.StatusTimeout)
				relayMgr.Remove(id)
			}
		}
	}()

	log.Printf("Starting remote-comfy gateway on :%s", port)
	r.Run(":" + port)
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
