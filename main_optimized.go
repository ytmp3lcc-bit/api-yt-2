package main

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "log"
    "net/http"
    "os"
    "os/signal"
    "os/exec"
    "path/filepath"
    "sort"
    "strings"
    "sync"
    "sync/atomic"
    "syscall"
    "time"

    "github.com/google/uuid"
    "github.com/redis/go-redis/v9"
    "golang.org/x/time/rate"
)

// --- Enhanced Data Structures ---

// Metadata structure for response
type Metadata struct {
	Title    string  `json:"title"`
	Uploader string  `json:"uploader"`
	Duration float64 `json:"duration"`
	AudioURL string  `json:"audio_url"`
	Ext      string  `json:"ext"`
	Abr      int     `json:"abr"`
}

// Request body structure
type Request struct {
	URL string `json:"url"`
}

// JobStatus represents the current state of a conversion job
type JobStatus string

const (
	StatusPending    JobStatus = "pending"
	StatusProcessing JobStatus = "processing"
	StatusCompleted  JobStatus = "completed"
	StatusFailed     JobStatus = "failed"
)

// ConversionJob holds information about a single conversion request
type ConversionJob struct {
	ID           string     `json:"id"`
	URL          string     `json:"url"`
	Status       JobStatus  `json:"status"`
	CreatedAt    time.Time  `json:"created_at"`
	StartedAt    time.Time  `json:"started_at"`
	CompletedAt  time.Time  `json:"completed_at"`
	FilePath     string     `json:"file_path"`
	DownloadURL  string     `json:"download_url"`
	Error        string     `json:"error"`
	Metadata     *Metadata  `json:"metadata"`
	Retries      int        `json:"retries"`
	MaxRetries   int        `json:"max_retries"`
	Priority     int        `json:"priority"` // Higher number = higher priority
}

// HealthStatus represents server health information
type HealthStatus struct {
	Status        string `json:"status"`
	ActiveJobs    int64  `json:"active_jobs"`
	QueuedJobs    int64  `json:"queued_jobs"`
	CompletedJobs int64  `json:"completed_jobs"`
	FailedJobs    int64  `json:"failed_jobs"`
	Workers       int    `json:"workers"`
	Uptime        string `json:"uptime"`
	MemoryUsage   string `json:"memory_usage"`
}

// --- High Traffic Configuration ---
const (
	// Worker Configuration
	WorkerPoolSize     = 20    // Increased from 5 to 20
	MaxJobRetries      = 3
	JobQueueCapacity   = 1000  // Increased from 100 to 1000
	
	// Rate Limiting
	RequestsPerSecond  = 100   // Rate limit for new requests
	BurstSize          = 200   // Burst capacity
	
	// Redis Configuration
	RedisAddr          = "localhost:6379"
	RedisPassword      = ""
	RedisDB            = 0
	
	// Job Expiration
	JobExpirationHours = 24    // Jobs expire after 24 hours
	
    // Health Check
    HealthCheckInterval = 30 * time.Second
    
    // Fast-path response: wait up to this duration for a job to finish
    FastPathWait = 8 * time.Second
)

// --- Global Variables ---
var (
	jobQueue     chan *ConversionJob
	jobStore     = struct {
		sync.RWMutex
		jobs map[string]*ConversionJob
	}{
		jobs: make(map[string]*ConversionJob),
	}
	
	// Metrics
	activeJobs    int64
	queuedJobs    int64
	completedJobs int64
	failedJobs    int64
	
	// Rate limiter
	rateLimiter = rate.NewLimiter(rate.Limit(RequestsPerSecond), BurstSize)
	
	// Redis client
	redisClient *redis.Client
	
	// Server start time
	serverStartTime = time.Now()
	
	// Context for graceful shutdown
	ctx, cancel = context.WithCancel(context.Background())
)

// Waiters notified when a job reaches a terminal state (completed or failed).
var jobWaiters = struct {
    sync.Mutex
    m map[string][]chan *ConversionJob
}{ m: make(map[string][]chan *ConversionJob) }

// --- Main Server Setup ---
func main() {
	// Initialize Redis
	initRedis()
	
	// Initialize job queue
	jobQueue = make(chan *ConversionJob, JobQueueCapacity)
	
	// Start worker pool
	for i := 0; i < WorkerPoolSize; i++ {
		go startWorker(i)
	}
	
	// Start health check routine
	go startHealthCheck()
	
	// Start job cleanup routine
	go startJobCleanup()
	
	// Setup HTTP routes with middleware
	mux := http.NewServeMux()
	mux.HandleFunc("/extract", rateLimitMiddleware(handleExtract))
	mux.HandleFunc("/status/", rateLimitMiddleware(handleStatus))
	mux.HandleFunc("/download/", rateLimitMiddleware(handleDownload))
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/metrics", handleMetrics)
	mux.HandleFunc("/stats", handleStats)
	
	// Setup graceful shutdown
	setupGracefulShutdown()
	
	fmt.Printf("🚀 High-Traffic Server running on http://localhost:8080 with %d workers\n", WorkerPoolSize)
	fmt.Printf("📊 Rate Limit: %d req/s (burst: %d)\n", RequestsPerSecond, BurstSize)
	fmt.Printf("💾 Redis: %s\n", RedisAddr)
	
	log.Fatal(http.ListenAndServe(":8080", mux))
}

// --- Redis Functions ---
func initRedis() {
	redisClient = redis.NewClient(&redis.Options{
		Addr:     RedisAddr,
		Password: RedisPassword,
		DB:       RedisDB,
	})
	
	// Test Redis connection
	_, err := redisClient.Ping(ctx).Result()
	if err != nil {
		log.Printf("⚠️  Redis not available, using in-memory storage: %v", err)
		redisClient = nil
	} else {
		log.Println("✅ Redis connected successfully")
	}
}

func saveJobToRedis(job *ConversionJob) error {
	if redisClient == nil {
		return nil
	}
	
	jobData, err := json.Marshal(job)
	if err != nil {
		return err
	}
	
	key := fmt.Sprintf("job:%s", job.ID)
	return redisClient.Set(ctx, key, jobData, JobExpirationHours*time.Hour).Err()
}

func getJobFromRedis(jobID string) (*ConversionJob, error) {
	if redisClient == nil {
		return nil, nil
	}
	
	key := fmt.Sprintf("job:%s", jobID)
	val, err := redisClient.Get(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	
	var job ConversionJob
	err = json.Unmarshal([]byte(val), &job)
	return &job, err
}

// --- Middleware ---
func rateLimitMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !rateLimiter.Allow() {
			http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next(w, r)
	}
}

func enableCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

// --- Handlers ---
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

	var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.URL == "" {
		http.Error(w, "Missing YouTube URL", http.StatusBadRequest)
		return
	}

	// Check if job already exists for this URL (deduplication)
	existingJob := findJobByURL(req.URL)
	if existingJob != nil && existingJob.Status == StatusCompleted {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"job_id": existingJob.ID,
			"status": string(existingJob.Status),
			"download_url": existingJob.DownloadURL,
			"check_status_endpoint": fmt.Sprintf("http://localhost:8080/status/%s", existingJob.ID),
		})
		return
	}

	jobID := uuid.New().String()
	job := &ConversionJob{
		ID:         jobID,
		URL:        req.URL,
		Status:     StatusPending,
		CreatedAt:  time.Now(),
		MaxRetries: MaxJobRetries,
		Priority:   1, // Default priority
	}

	// Save to both memory and Redis
	jobStore.Lock()
	jobStore.jobs[jobID] = job
	jobStore.Unlock()
	
	saveJobToRedis(job)
	atomic.AddInt64(&queuedJobs, 1)

    // Register a waiter to enable fast-path response if the job finishes quickly
    resultCh := registerJobWaiter(jobID)
    // Add job to the queue
    select {
    case jobQueue <- job:
        fmt.Printf("✅ Job %s added to queue for URL: %s\n", jobID, req.URL)
        w.Header().Set("Content-Type", "application/json")
        // Wait briefly to see if the job completes quickly
        select {
        case doneJob := <-resultCh:
            if doneJob.Status == StatusCompleted {
                json.NewEncoder(w).Encode(map[string]string{
                    "job_id": jobID,
                    "status": string(doneJob.Status),
                    "download_url": doneJob.DownloadURL,
                    "check_status_endpoint": fmt.Sprintf("http://localhost:8080/status/%s", jobID),
                })
            } else {
                json.NewEncoder(w).Encode(map[string]interface{}{
                    "job_id": jobID,
                    "status": string(doneJob.Status),
                    "error": doneJob.Error,
                    "check_status_endpoint": fmt.Sprintf("http://localhost:8080/status/%s", jobID),
                })
            }
        case <-time.After(FastPathWait):
            // Timed out waiting; unregister waiter to avoid leaks
            unregisterJobWaiter(jobID, resultCh)
            json.NewEncoder(w).Encode(map[string]string{
                "job_id": jobID,
                "status": string(job.Status),
                "check_status_endpoint": fmt.Sprintf("http://localhost:8080/status/%s", jobID),
            })
        }
    default:
        // Queue is full
        unregisterJobWaiter(jobID, resultCh)
        jobStore.Lock()
        delete(jobStore.jobs, jobID)
        jobStore.Unlock()
        atomic.AddInt64(&queuedJobs, -1)
        http.Error(w, "Server busy, please try again later.", http.StatusServiceUnavailable)
        fmt.Printf("❌ Job %s for URL %s rejected, queue full.\n", jobID, req.URL)
    }
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	jobID := filepath.Base(r.URL.Path)
	if jobID == "" {
		http.Error(w, "Missing job ID", http.StatusBadRequest)
		return
	}

    // Try Redis first, then memory
    job, err := getJobFromRedis(jobID)
    if err != nil || job == nil {
        jobStore.RLock()
        jobMem, exists := jobStore.jobs[jobID]
        jobStore.RUnlock()
        if !exists {
            http.Error(w, "Job not found", http.StatusNotFound)
            return
        }
        job = jobMem
    }

	response := struct {
		JobID        string    `json:"job_id"`
		Status       JobStatus `json:"status"`
		Progress     string    `json:"progress,omitempty"`
		DownloadURL  string    `json:"download_url,omitempty"`
		Error        string    `json:"error,omitempty"`
		Metadata     *Metadata `json:"metadata,omitempty"`
		CreatedAt    time.Time `json:"created_at"`
		CompletedAt  time.Time `json:"completed_at,omitempty"`
	}{
		JobID:       job.ID,
		Status:      job.Status,
		DownloadURL: job.DownloadURL,
		Error:       job.Error,
		Metadata:    job.Metadata,
		CreatedAt:   job.CreatedAt,
		CompletedAt: job.CompletedAt,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func handleDownload(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	filenameWithExt := filepath.Base(r.URL.Path)
	jobID := filenameWithExt[:len(filenameWithExt)-len(".mp3")]

	// Try Redis first, then memory
	job, err := getJobFromRedis(jobID)
	if err != nil || job == nil {
		jobStore.RLock()
		job, exists := jobStore.jobs[jobID]
		jobStore.RUnlock()
		
		if !exists || job.Status != StatusCompleted {
			http.Error(w, "File not found or conversion not completed", http.StatusNotFound)
			return
		}
	}

	if job.FilePath == "" {
		http.Error(w, "File path not available", http.StatusInternalServerError)
		return
	}

	file, err := os.Open(job.FilePath)
	if err != nil {
		http.Error(w, "Error opening file", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	fmt.Printf("⬇️  File requested for download: %s (Job ID: %s)\n", filenameWithExt, jobID)

	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filenameWithExt))
	w.Header().Set("Cache-Control", "public, max-age=3600") // Cache for 1 hour
	io.Copy(w, file)
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	
	status := "healthy"
	if atomic.LoadInt64(&activeJobs) > WorkerPoolSize*2 {
		status = "overloaded"
	}
	
	health := HealthStatus{
		Status:        status,
		ActiveJobs:    atomic.LoadInt64(&activeJobs),
		QueuedJobs:    atomic.LoadInt64(&queuedJobs),
		CompletedJobs: atomic.LoadInt64(&completedJobs),
		FailedJobs:    atomic.LoadInt64(&failedJobs),
		Workers:       WorkerPoolSize,
		Uptime:        time.Since(serverStartTime).String(),
		MemoryUsage:   getMemoryUsage(),
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(health)
}

func handleMetrics(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	
	metrics := map[string]interface{}{
		"active_jobs":    atomic.LoadInt64(&activeJobs),
		"queued_jobs":    atomic.LoadInt64(&queuedJobs),
		"completed_jobs": atomic.LoadInt64(&completedJobs),
		"failed_jobs":    atomic.LoadInt64(&failedJobs),
		"workers":        WorkerPoolSize,
		"queue_capacity": JobQueueCapacity,
		"rate_limit":     RequestsPerSecond,
		"uptime_seconds": time.Since(serverStartTime).Seconds(),
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(metrics)
}

func handleStats(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	
	jobStore.RLock()
	totalJobs := len(jobStore.jobs)
	jobStore.RUnlock()
	
	stats := map[string]interface{}{
		"total_jobs":     totalJobs,
		"active_jobs":    atomic.LoadInt64(&activeJobs),
		"queued_jobs":    atomic.LoadInt64(&queuedJobs),
		"completed_jobs": atomic.LoadInt64(&completedJobs),
		"failed_jobs":    atomic.LoadInt64(&failedJobs),
		"success_rate":   calculateSuccessRate(),
		"avg_processing_time": getAvgProcessingTime(),
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// --- Worker Logic ---
func startWorker(workerID int) {
	fmt.Printf("Worker %d started.\n", workerID)
	for job := range jobQueue {
		processJob(job, workerID)
	}
}

func processJob(job *ConversionJob, workerID int) {
	atomic.AddInt64(&activeJobs, 1)
	atomic.AddInt64(&queuedJobs, -1)
	
	log.Printf("Worker %d: Processing job %s for URL: %s\n", workerID, job.ID, job.URL)

	// Update job status
	updateJobStatus(job, StatusProcessing, "")
	job.StartedAt = time.Now()

	outputDir := "downloads"
	if err := os.MkdirAll(outputDir, os.ModePerm); err != nil {
		updateJobStatus(job, StatusFailed, fmt.Sprintf("Error creating downloads directory: %v", err))
		atomic.AddInt64(&activeJobs, -1)
		atomic.AddInt64(&failedJobs, 1)
		return
	}
	outputPath := filepath.Join(outputDir, job.ID+".mp3")

	// Extract audio stream URL via yt-dlp
	audioURL, meta, err := getAudioStreamFromYTDLP(job.URL)
	if err != nil {
		handleJobFailure(job, err, "yt-dlp stream extraction failed")
		atomic.AddInt64(&activeJobs, -1)
		atomic.AddInt64(&failedJobs, 1)
		return
	}

	// Convert stream to MP3 file using ffmpeg
	err = convertStreamToMP3(audioURL, outputPath)
	if err != nil {
		handleJobFailure(job, err, "ffmpeg conversion failed")
		atomic.AddInt64(&activeJobs, -1)
		atomic.AddInt64(&failedJobs, 1)
		return
	}

	// Job Completed Successfully
	job.Status = StatusCompleted
	job.CompletedAt = time.Now()
	job.FilePath = outputPath
	job.DownloadURL = fmt.Sprintf("http://localhost:8080/download/%s.mp3", job.ID)
	job.Metadata = meta
	job.Error = ""
	
	// Save to Redis
	saveJobToRedis(job)
	
	atomic.AddInt64(&activeJobs, -1)
	atomic.AddInt64(&completedJobs, 1)

	log.Printf("Worker %d: Job %s completed successfully. Download: %s\n", workerID, job.ID, job.DownloadURL)
}

// --- Helper Functions ---
// --- Job completion waiter management ---
func registerJobWaiter(jobID string) chan *ConversionJob {
    ch := make(chan *ConversionJob, 1)
    jobWaiters.Lock()
    jobWaiters.m[jobID] = append(jobWaiters.m[jobID], ch)
    jobWaiters.Unlock()
    return ch
}

func notifyJobCompletion(job *ConversionJob) {
    jobWaiters.Lock()
    waiters := jobWaiters.m[job.ID]
    delete(jobWaiters.m, job.ID)
    jobWaiters.Unlock()
    for _, ch := range waiters {
        select {
        case ch <- job:
        default:
        }
        close(ch)
    }
}

func unregisterJobWaiter(jobID string, ch chan *ConversionJob) {
    jobWaiters.Lock()
    defer jobWaiters.Unlock()
    waiters := jobWaiters.m[jobID]
    for i, c := range waiters {
        if c == ch {
            jobWaiters.m[jobID] = append(waiters[:i], waiters[i+1:]...)
            break
        }
    }
    if len(jobWaiters.m[jobID]) == 0 {
        delete(jobWaiters.m, jobID)
    }
    close(ch)
}
func updateJobStatus(job *ConversionJob, status JobStatus, errMsg string) {
	job.Status = status
	job.Error = errMsg
	if status == StatusFailed {
		job.CompletedAt = time.Now()
	}
	
	// Update in memory
	jobStore.Lock()
	jobStore.jobs[job.ID] = job
	jobStore.Unlock()
	
	// Update in Redis
	saveJobToRedis(job)
	
	log.Printf("Job %s status updated to %s: %s\n", job.ID, status, errMsg)
}

func handleJobFailure(job *ConversionJob, err error, stage string) {
	job.Retries++
	if job.Retries <= job.MaxRetries {
		log.Printf("Job %s (%s): %s. Retrying (%d/%d)...\n", job.ID, job.URL, err.Error(), job.Retries, job.MaxRetries)
		time.Sleep(5 * time.Second)
		select {
		case jobQueue <- job:
			atomic.AddInt64(&queuedJobs, 1)
		default:
			updateJobStatus(job, StatusFailed, fmt.Sprintf("%s: %v. Max retries exceeded and queue full.", stage, err))
			notifyJobCompletion(job)
		}
    } else {
        updateJobStatus(job, StatusFailed, fmt.Sprintf("%s: %v. Max retries (%d) exceeded.", stage, err, job.MaxRetries))
        notifyJobCompletion(job)
    }
}

func findJobByURL(url string) *ConversionJob {
	jobStore.RLock()
	defer jobStore.RUnlock()
	
	for _, job := range jobStore.jobs {
		if job.URL == url && job.Status == StatusCompleted {
			return job
		}
	}
	return nil
}

func startHealthCheck() {
	ticker := time.NewTicker(HealthCheckInterval)
	defer ticker.Stop()
	
	for {
		select {
		case <-ticker.C:
			// Log current stats
			log.Printf("📊 Stats - Active: %d, Queued: %d, Completed: %d, Failed: %d",
				atomic.LoadInt64(&activeJobs),
				atomic.LoadInt64(&queuedJobs),
				atomic.LoadInt64(&completedJobs),
				atomic.LoadInt64(&failedJobs))
		case <-ctx.Done():
			return
		}
	}
}

func startJobCleanup() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	
	for {
		select {
		case <-ticker.C:
			cleanupOldJobs()
		case <-ctx.Done():
			return
		}
	}
}

func cleanupOldJobs() {
	cutoff := time.Now().Add(-JobExpirationHours * time.Hour)
	
	jobStore.Lock()
	for id, job := range jobStore.jobs {
		if job.CreatedAt.Before(cutoff) {
			// Remove file if it exists
			if job.FilePath != "" {
				os.Remove(job.FilePath)
			}
			delete(jobStore.jobs, id)
		}
	}
	jobStore.Unlock()
	
	log.Printf("🧹 Cleaned up old jobs older than %d hours", JobExpirationHours)
}

func setupGracefulShutdown() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	
	go func() {
		<-c
		log.Println("🛑 Graceful shutdown initiated...")
		
		// Cancel context
		cancel()
		
		// Close job queue
		close(jobQueue)
		
		// Wait for workers to finish
		time.Sleep(5 * time.Second)
		
		log.Println("✅ Graceful shutdown completed")
		os.Exit(0)
	}()
}

func getMemoryUsage() string {
	// This is a simplified memory usage calculation
	// In production, you'd use runtime.MemStats
	return "N/A"
}

func calculateSuccessRate() float64 {
	completed := atomic.LoadInt64(&completedJobs)
	failed := atomic.LoadInt64(&failedJobs)
	total := completed + failed
	
	if total == 0 {
		return 0
	}
	return float64(completed) / float64(total) * 100
}

func getAvgProcessingTime() float64 {
	// This would require tracking processing times
	// For now, return a placeholder
	return 0
}

// --- External Tool Functions (unchanged) ---
func getAudioStreamFromYTDLP(videoURL string) (string, *Metadata, error) {
    // 1) Query yt-dlp for full format metadata (no download)
    ctxTimeout, cancel := context.WithTimeout(ctx, 45*time.Second)
    defer cancel()

    cmd := exec.CommandContext(ctxTimeout, "yt-dlp", "-J", "--no-warnings", "--skip-download", videoURL)
    var stdout, stderr bytes.Buffer
    cmd.Stdout = &stdout
    cmd.Stderr = &stderr
    if err := cmd.Run(); err != nil {
        return "", nil, fmt.Errorf("yt-dlp metadata error: %v | %s", err, strings.TrimSpace(stderr.String()))
    }

    // Parse JSON
    type ytdlpFormat struct {
        FormatID string  `json:"format_id"`
        ACodec   string  `json:"acodec"`
        VCodec   string  `json:"vcodec"`
        Ext      string  `json:"ext"`
        Protocol string  `json:"protocol"`
        URL      string  `json:"url"`
        ABR      float64 `json:"abr"`
        TBR      float64 `json:"tbr"`
    }
    type ytdlpInfo struct {
        Title    string        `json:"title"`
        Uploader string        `json:"uploader"`
        Duration float64       `json:"duration"`
        Formats  []ytdlpFormat `json:"formats"`
    }

    var info ytdlpInfo
    if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
        return "", nil, fmt.Errorf("yt-dlp metadata parse error: %v", err)
    }

    // 2) Select best playable audio format with robust fallbacks
    candidates := make([]ytdlpFormat, 0, len(info.Formats))
    for _, f := range info.Formats {
        if f.URL == "" {
            continue
        }
        // Prefer audio-only, but allow progressive as last resort
        isAudioOnly := (f.VCodec == "none" || f.VCodec == "") && f.ACodec != "none"
        if isAudioOnly {
            candidates = append(candidates, f)
            continue
        }
    }
    if len(candidates) == 0 {
        // No audio-only; allow progressive streams
        for _, f := range info.Formats {
            if f.URL == "" {
                continue
            }
            if f.ACodec != "none" { // has audio track
                candidates = append(candidates, f)
            }
        }
    }
    if len(candidates) == 0 {
        return "", nil, fmt.Errorf("no usable audio formats found")
    }

    // Rank candidates
    sort.SliceStable(candidates, func(i, j int) bool {
        si := scoreFormat(formatInfoForScore{Ext: candidates[i].Ext, Protocol: candidates[i].Protocol, ABR: candidates[i].ABR, TBR: candidates[i].TBR})
        sj := scoreFormat(formatInfoForScore{Ext: candidates[j].Ext, Protocol: candidates[j].Protocol, ABR: candidates[j].ABR, TBR: candidates[j].TBR})
        if si == sj {
            return candidates[i].ABR > candidates[j].ABR
        }
        return si > sj
    })

    best := candidates[0]
    meta := &Metadata{
        Title:    info.Title,
        Uploader: info.Uploader,
        Duration: info.Duration,
        AudioURL: best.URL,
        Ext:      best.Ext,
        Abr:      int(best.ABR),
    }
    return best.URL, meta, nil
}

func convertStreamToMP3(audioURL, outputPath string) error {
    // Convert the remote stream/manifest to MP3 using ffmpeg
    ctxTimeout, cancel := context.WithTimeout(ctx, 10*time.Minute)
    defer cancel()

    args := []string{
        "-y", // overwrite
        "-loglevel", "error",
        "-nostdin",
        "-i", audioURL,
        "-vn",
        "-acodec", "libmp3lame",
        "-ar", "44100",
        "-b:a", "192k",
        outputPath,
    }
    cmd := exec.CommandContext(ctxTimeout, "ffmpeg", args...)
    var stderr bytes.Buffer
    cmd.Stderr = &stderr
    if err := cmd.Run(); err != nil {
        return fmt.Errorf("ffmpeg error: %v | %s", err, strings.TrimSpace(stderr.String()))
    }
    return nil
}

// scoreFormat assigns a preference score to a yt-dlp format for audio extraction
type formatInfoForScore struct {
    Ext      string
    Protocol string
    ABR      float64
    TBR      float64
}

func scoreFormat(f formatInfoForScore) int {
    score := 0
    switch strings.ToLower(f.Ext) {
    case "m4a":
        score += 100
    case "webm":
        score += 90
    case "ogg", "opus":
        score += 85
    case "mp4":
        score += 70
    default:
        score += 60
    }
    p := strings.ToLower(f.Protocol)
    if strings.HasPrefix(p, "https") {
        score += 30
    } else if strings.HasPrefix(p, "http") {
        score += 25
    } else if strings.Contains(p, "m3u8") || strings.Contains(p, "hls") {
        score += 20
    } else if strings.Contains(p, "dash") {
        score += 15
    }
    // Prefer higher bitrate when available
    if f.ABR > 0 {
        score += int(f.ABR)
    } else if f.TBR > 0 {
        score += int(f.TBR / 2)
    }
    return score
}