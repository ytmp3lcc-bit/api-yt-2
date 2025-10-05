// api-gateway/main.go
package main

import (
    "encoding/json"
    "fmt"
    "log"
    "net/http"
    "net/url"
    "os"
    "path/filepath"
    "strings"
    "time"

    "youtube-audio-api-scalable/shared" // Import shared package

    "github.com/google/uuid"
)

// Global instances for our conceptual database and message queue
var (
	cfg *shared.Config
	db  shared.DatabaseClient
	mq  shared.MessageQueueClient
    rl  *shared.RateLimiter
)

func main() {
	cfg = shared.LoadConfig()
	if cfg.APIGatewayPort == "" {
		cfg.APIGatewayPort = shared.DefaultAPIGatewayPort
	}
	log.Printf("API Gateway starting on port %s", cfg.APIGatewayPort)

    // Try Redis-backed DB and Queue first; fallback to in-memory
    redisClient := shared.NewRedisClient(cfg)
    if err := shared.PingRedis(redisClient); err == nil && redisClient != nil {
        db = shared.NewRedisDB(redisClient)
        mq = shared.NewRedisQueue(redisClient, cfg.QueueName, cfg.QueueMaxLength)
        log.Println("Initialized Redis-backed DB and Queue.")
    } else {
        db = shared.NewInMemoryDB()
        mq = shared.NewInMemoryQueue(100)
        log.Println("Initialized in-memory DB and Queue (Redis not configured/reachable).")
    }
    defer mq.Close() // Ensure the queue is closed on shutdown

    // Rate limiter
    rl = shared.NewRateLimiter(cfg, redisClient)

    // Ensure output directory exists for downloads
    if err := os.MkdirAll(shared.OutputDir, os.ModePerm); err != nil {
        log.Fatalf("Failed to create output dir: %v", err)
    }

	http.HandleFunc("/extract", handleExtract)
    http.HandleFunc("/status/", handleStatus)
    http.HandleFunc("/download/", handleDownload)
	http.HandleFunc("/health", handleHealth)

	// Admin endpoints (with a simple middleware for auth)
	adminRouter := http.NewServeMux()
	adminRouter.HandleFunc("/admin/jobs", handleAdminListJobs)
	adminRouter.HandleFunc("/admin/jobs/", handleAdminGetJob)
	adminRouter.HandleFunc("/admin/delete/", handleAdminDeleteJob)
	// adminRouter.HandleFunc("/admin/cache", handleAdminGetCache) // Cache endpoints for later
	// adminRouter.HandleFunc("/admin/cache/clear", handleAdminClearCache)

	http.Handle("/admin/", adminAuthMiddleware(adminRouter))

	fmt.Printf("🚀 API Gateway Server running on http://localhost:%s\n", cfg.APIGatewayPort)
	log.Fatal(http.ListenAndServe(":"+cfg.APIGatewayPort, nil))
}

// Enable CORS for browser requests
func enableCORS(w http.ResponseWriter) {
    // For simplicity, allow '*' unless specific origins configured
    origin := "*"
    if len(cfg.AllowedOrigins) == 1 && cfg.AllowedOrigins[0] == "*" {
        origin = "*"
    } else {
        // In production, you would reflect the Origin if it's in the allowlist
        origin = strings.Join(cfg.AllowedOrigins, ",")
    }
    w.Header().Set("Access-Control-Allow-Origin", origin)
    w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, DELETE")
    w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
    w.Header().Set("Vary", "Origin")
    w.Header().Set("Access-Control-Max-Age", "600")
}

// adminAuthMiddleware provides a basic bearer token authentication for admin routes
func adminAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		enableCORS(w) // CORS for admin too
		if r.Method == http.MethodOptions {
            w.WriteHeader(http.StatusOK)
			return
		}

		token := r.Header.Get("Authorization")
        if strings.TrimSpace(cfg.AdminToken) == "" {
            http.Error(w, "Admin token not configured", http.StatusServiceUnavailable)
            return
        }
        if token != "Bearer "+cfg.AdminToken { // Simple bearer token auth
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// handleExtract: Starts a job, pushes to queue, and returns immediately
func handleExtract(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	if r.Method == http.MethodOptions {
        w.WriteHeader(http.StatusOK)
        return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}

	var req shared.Request // Use shared.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
    if req.URL == "" {
		http.Error(w, "Missing YouTube URL", http.StatusBadRequest)
		return
	}

    // Basic URL validation and allowed host check
    parsed, err := url.Parse(req.URL)
    if err != nil || parsed.Scheme == "" || parsed.Host == "" {
        http.Error(w, "Invalid URL", http.StatusBadRequest)
        return
    }
    allowed := false
    host := strings.ToLower(parsed.Host)
    for _, h := range cfg.AllowedVideoHosts {
        if h == "*" || strings.HasSuffix(host, strings.ToLower(h)) {
            allowed = true
            break
        }
    }
    if !allowed {
        http.Error(w, "Host not allowed", http.StatusBadRequest)
        return
    }

    // Simple rate limiting per IP
    ip := shared.GetClientIP(r)
    if ok, _ := rl.Allow(ip); !ok {
        http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
        return
    }

	jobID := uuid.New().String()
	now := time.Now()
	job := &shared.Job{ // Use shared.Job
		ID:          jobID,
		OriginalURL: req.URL,
		Status:      shared.JobStatusPending,
		CreatedAt:   now,
	}

	// 1. Store initial job status in DB
	if err := db.CreateJob(job); err != nil {
		log.Printf("ERROR: Failed to create job %s in DB: %v", jobID, err)
		http.Error(w, "Failed to initialize job", http.StatusInternalServerError)
		return
	}
	log.Printf("INFO: Job %s created in DB with status %s", jobID, job.Status)

    // 2. Publish job to message queue
	jobMessage := shared.JobMessage{
		JobID:       jobID,
		OriginalURL: req.URL,
	}
	if err := mq.Publish(jobMessage); err != nil {
		log.Printf("ERROR: Failed to publish job %s to queue: %v", jobID, err)
		// Mark job as failed in DB since it couldn't be queued
		job.Status = shared.JobStatusFailed
		job.Error = fmt.Sprintf("Failed to queue job: %v", err)
		db.UpdateJob(job) // Attempt to update status in DB
		http.Error(w, "Failed to submit job to processing queue", http.StatusInternalServerError)
		return
	}
	log.Printf("INFO: Job %s published to message queue", jobID)

	// 3. Respond immediately to client
	w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]string{
        "job_id":       jobID,
        "status":       string(job.Status),
        "message":      "Audio extraction started. Check status at /status/" + jobID,
        "instructions": "A worker service will process this job and update its status. Polling /status/{job_id} is recommended.",
    })
	fmt.Printf("🎬 API Gateway received job %s for URL: %s\n", jobID, req.URL)
}

// handleDownload: Streams the generated MP3 file to the client
func handleDownload(w http.ResponseWriter, r *http.Request) {
    enableCORS(w)
    if r.Method == http.MethodOptions {
        w.WriteHeader(http.StatusOK)
        return
    }
    if r.Method != http.MethodGet {
        http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
        return
    }
    jobID := filepath.Base(r.URL.Path)
    job, err := db.GetJob(jobID)
    if err != nil || job.Status != shared.JobStatusCompleted || job.FilePath == "" {
        http.Error(w, "File not available", http.StatusNotFound)
        return
    }
    // Serve file with appropriate headers
    w.Header().Set("Content-Type", "audio/mpeg")
    w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s.mp3\"", jobID))
    // Let http.ServeFile handle range requests and efficient serving
    http.ServeFile(w, r, job.FilePath)
}

// handleStatus: Checks job status from the database
func handleStatus(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	if r.Method == http.MethodOptions {
        w.WriteHeader(http.StatusOK)
        return
	}
    if r.Method != http.MethodGet {
        http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
        return
    }

	jobID := filepath.Base(r.URL.Path) // Extract job ID from /status/{job_id}

	job, err := db.GetJob(jobID)
	if err != nil {
		http.Error(w, "Job not found", http.StatusNotFound)
		return
	}

    // For completed jobs, include a direct download URL if not set
    if job.Status == shared.JobStatusCompleted && job.DownloadEndpoint == "" {
        base := cfg.PublicAPIBaseURL
        if strings.TrimSpace(base) == "" {
            base = fmt.Sprintf("http://localhost:%s", cfg.APIGatewayPort)
        }
        job.DownloadEndpoint = fmt.Sprintf("%s/download/%s", strings.TrimRight(base, "/"), jobID)
    }

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(job)
}

// handleHealth: Basic health check for the API Gateway
func handleHealth(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	if r.Method == http.MethodOptions {
        w.WriteHeader(http.StatusOK)
        return
	}

    // Perform lightweight dependency checks
    status := "ok"
    details := map[string]string{}
    if rc := shared.NewRedisClient(cfg); rc != nil {
        if err := shared.PingRedis(rc); err != nil {
            status = "degraded"
            details["redis"] = "unreachable"
        } else {
            details["redis"] = "ok"
        }
    } else {
        details["redis"] = "not_configured"
    }
	w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]any{
        "status":  status,
        "details": details,
    })
}

// handleAdminListJobs: Lists all jobs from the database
func handleAdminListJobs(w http.ResponseWriter, r *http.Request) {
	// Auth handled by middleware
    enableCORS(w)
    if r.Method == http.MethodOptions {
        w.WriteHeader(http.StatusOK)
        return
    }
    if r.Method != http.MethodGet {
        http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
        return
    }
	jobs, err := db.GetAllJobs()
	if err != nil {
		log.Printf("ERROR: Failed to get all jobs for admin: %v", err)
		http.Error(w, "Failed to retrieve jobs", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(jobs)
}

// handleAdminGetJob: Get details for a specific job from the database
func handleAdminGetJob(w http.ResponseWriter, r *http.Request) {
	// Auth handled by middleware
    enableCORS(w)
    if r.Method == http.MethodOptions {
        w.WriteHeader(http.StatusOK)
        return
    }
    if r.Method != http.MethodGet {
        http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
        return
    }
	jobID := filepath.Base(r.URL.Path) // Extract job ID from /admin/jobs/{job_id}

	job, err := db.GetJob(jobID)
	if err != nil {
		http.Error(w, "Job not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(job)
}

// handleAdminDeleteJob: Deletes a job from the database and conceptually removes its file
func handleAdminDeleteJob(w http.ResponseWriter, r *http.Request) {
	// Auth handled by middleware
	if r.Method == http.MethodOptions {
        w.WriteHeader(http.StatusOK)
        return
	}
	if r.Method != http.MethodDelete {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}

	jobID := filepath.Base(r.URL.Path) // Extract job ID from /admin/delete/{job_id}

	job, err := db.GetJob(jobID)
	if err != nil {
		http.Error(w, "Job not found", http.StatusNotFound)
		return
	}

	// Conceptual file deletion (in a real system, this would interact with Object Storage)
    if job.FilePath != "" {
        // Delete the actual stored file
        fullPath := job.FilePath
        if _, statErr := os.Stat(fullPath); statErr == nil { // Check if file exists
            if rmErr := os.Remove(fullPath); rmErr != nil {
                log.Printf("WARN: Failed to delete local file %s for job %s: %v", fullPath, jobID, rmErr)
            } else {
                log.Printf("INFO: Deleted local file: %s", fullPath)
            }
        }
    }

	if err := db.DeleteJob(jobID); err != nil {
		log.Printf("ERROR: Failed to delete job %s from DB: %v", jobID, err)
		http.Error(w, "Failed to delete job", http.StatusInternalServerError)
		return
	}
	log.Printf("INFO: Deleted job %s from DB", jobID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"message": fmt.Sprintf("Job %s and associated file (if existed) deleted successfully.", jobID),
	})
}
