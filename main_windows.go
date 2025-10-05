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
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"
)

// --- Data Structures ---

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
	Priority     int        `json:"priority"`
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
	WorkerPoolSize     = 10    // Reduced for Windows
	MaxJobRetries      = 3
	JobQueueCapacity   = 500   // Reduced for Windows
	
	// Rate Limiting
	RequestsPerSecond  = 50    // Reduced for Windows
	BurstSize          = 100   // Reduced for Windows
	
	// Job Expiration
	JobExpirationHours = 24
	
	// Health Check
	HealthCheckInterval = 30 * time.Second
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
	
	// Server start time
	serverStartTime = time.Now()
	
	// Context for graceful shutdown
	ctx, cancel = context.WithCancel(context.Background())
)

// --- Main Server Setup ---
func main() {
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
	
	// Setup HTTP routes
	mux := http.NewServeMux()
	mux.HandleFunc("/extract", handleExtract)
	mux.HandleFunc("/status/", handleStatus)
	mux.HandleFunc("/download/", handleDownload)
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/metrics", handleMetrics)
	mux.HandleFunc("/stats", handleStats)
	
	// Setup graceful shutdown
	setupGracefulShutdown()
	
	fmt.Printf("🚀 High-Traffic Server running on http://localhost:8080 with %d workers\n", WorkerPoolSize)
	fmt.Printf("📊 Rate Limit: %d req/s (burst: %d)\n", RequestsPerSecond, BurstSize)
	fmt.Printf("💾 Storage: In-memory (Redis not available)\n")
	
	log.Fatal(http.ListenAndServe(":8080", mux))
}

// --- Middleware ---
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
		Priority:   1,
	}

	// Save to memory
	jobStore.Lock()
	jobStore.jobs[jobID] = job
	jobStore.Unlock()
	
	atomic.AddInt64(&queuedJobs, 1)

	// Add job to the queue
	select {
	case jobQueue <- job:
		fmt.Printf("✅ Job %s added to queue for URL: %s\n", jobID, req.URL)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"job_id": jobID,
			"status": string(job.Status),
			"check_status_endpoint": fmt.Sprintf("http://localhost:8080/status/%s", jobID),
		})
	default:
		// Queue is full
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

	jobStore.RLock()
	job, exists := jobStore.jobs[jobID]
	jobStore.RUnlock()
	
	if !exists {
		http.Error(w, "Job not found", http.StatusNotFound)
		return
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

	jobStore.RLock()
	job, exists := jobStore.jobs[jobID]
	jobStore.RUnlock()
	
	if !exists || job.Status != StatusCompleted {
		http.Error(w, "File not found or conversion not completed", http.StatusNotFound)
		return
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
	w.Header().Set("Cache-Control", "public, max-age=3600")
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
		MemoryUsage:   "N/A",
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
		"avg_processing_time": 0.0,
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
	
	atomic.AddInt64(&activeJobs, -1)
	atomic.AddInt64(&completedJobs, 1)

	log.Printf("Worker %d: Job %s completed successfully. Download: %s\n", workerID, job.ID, job.DownloadURL)
}

// --- Helper Functions ---
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
		}
	} else {
		updateJobStatus(job, StatusFailed, fmt.Sprintf("%s: %v. Max retries (%d) exceeded.", stage, err, job.MaxRetries))
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

func calculateSuccessRate() float64 {
	completed := atomic.LoadInt64(&completedJobs)
	failed := atomic.LoadInt64(&failedJobs)
	total := completed + failed
	
	if total == 0 {
		return 0
	}
	return float64(completed) / float64(total) * 100
}

// --- External Tool Functions ---
func getAudioStreamFromYTDLP(videoURL string) (string, *Metadata, error) {
	// Check if yt-dlp exists
	cmd := exec.Command("yt-dlp", "--version")
	if err := cmd.Run(); err != nil {
		return "", nil, fmt.Errorf("yt-dlp not found. Please install yt-dlp first")
	}

	cmd = exec.Command("yt-dlp", "-f", "bestaudio", "--dump-json", "--no-warnings", videoURL)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Run(); err != nil {
		return "", nil, fmt.Errorf("yt-dlp failed: %v\nOutput: %s", err, out.String())
	}

	var data struct {
		Title    string  `json:"title"`
		Uploader string  `json:"uploader"`
		Duration float64 `json:"duration"`
		URL      string  `json:"url"`
		Ext      string  `json:"ext"`
		Abr      float64 `json:"abr"`
	}

	if err := json.Unmarshal(out.Bytes(), &data); err != nil {
		return "", nil, fmt.Errorf("JSON parse error from yt-dlp output: %v\nOutput: %s", err, out.String())
	}

	if data.URL == "" {
		return "", nil, fmt.Errorf("no audio stream URL found in yt-dlp output for bestaudio format")
	}

	meta := &Metadata{
		Title:    data.Title,
		Uploader: data.Uploader,
		Duration: data.Duration,
		Ext:      data.Ext,
		Abr:      int(data.Abr),
	}

	return data.URL, meta, nil
}

func convertStreamToMP3(audioURL, outputPath string) error {
	// Check if ffmpeg exists
	cmd := exec.Command("ffmpeg", "-version")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg not found. Please install ffmpeg first")
	}

	start := time.Now()

	cmd = exec.Command("ffmpeg",
		"-y",
		"-i", audioURL,
		"-vn",
		"-ar", "44100",
		"-ac", "2",
		"-b:a", "192k",
		"-f", "mp3",
		"-preset", "veryfast",
		"-threads", "0",
		outputPath,
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg error: %v\nOutput: %s", err, out.String())
	}

	elapsed := time.Since(start)
	log.Printf("⏱️ FFmpeg conversion to MP3 time: %.2fs\n", elapsed.Seconds())

	return nil
}