package main

import (
	"log"
	"os"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"

	"github.com/kitsune-hash/remote-comfy/gateway/db"
	"github.com/kitsune-hash/remote-comfy/gateway/handlers"
	"github.com/kitsune-hash/remote-comfy/gateway/models"
	"github.com/kitsune-hash/remote-comfy/gateway/relay"
)

func main() {
	// Config
	port := getEnv("PORT", "8080")
	dbPath := getEnv("DB_PATH", "./data/remote-comfy.db")
	jobTimeout := 20 * time.Minute

	// Ensure data directory exists
	os.MkdirAll("./data", 0755)

	// Database
	store, err := db.New(dbPath)
	if err != nil {
		log.Fatalf("Failed to init database: %v", err)
	}
	defer store.Close()
	log.Println("Database initialized")

	// Relay manager
	relayMgr := relay.NewManager()

	// Handlers
	wfHandler := handlers.NewWorkflowHandler(store, relayMgr)

	// Router
	r := gin.Default()

	// CORS
	r.Use(cors.New(cors.Config{
		AllowAllOrigins:  true,
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Accept", "Authorization"},
		AllowWebSockets:  true,
		MaxAge:           12 * time.Hour,
	}))

	// Routes
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
