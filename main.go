package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log" // Temporarily for Redis connection, will switch to zap
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// --- Global Context for graceful shutdown ---
var (
	ctx    context.Context
	cancel context.CancelFunc
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
	// Optional webhook URL for callbacks
	WebhookURL string `json:"webhook_url,omitempty"`
}

// JobStatus represents the current state of a conversion job
type JobStatus string

const (
	StatusPending    JobStatus = "pending"
	StatusProcessing JobStatus = "processing"
	StatusCompleted  JobStatus = "completed"
	StatusFailed     JobStatus = "failed"
	StatusRetrying   JobStatus = "retrying"
)

// ConversionJob holds information about a single conversion request
type ConversionJob struct {
	ID           string    `json:"id"`
	URL          string    `json:"url"`
	Status       JobStatus `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	StartedAt    time.Time `json:"started_at,omitempty"`
	CompletedAt  time.Time `json:"completed_at,omitempty"`
	FilePath     string    `json:"file_path,omitempty"`      // Local path to the converted MP3
	DownloadURL  string    `json:"download_url,omitempty"`   // Public URL for download
	Error        string    `json:"error,omitempty"`          // Error message if conversion failed
	Metadata     *Metadata `json:"metadata,omitempty"`       // Extracted metadata
	Retries      int       `json:"retries"`                  // Number of retries attempted
	MaxRetries   int       `json:"max_retries"`              // Maximum allowed retries
	WebhookURL   string    `json:"webhook_url,omitempty"`    // Webhook URL for this specific job
}

// --- Configuration ---
const (
	RedisAddr         = "localhost:6379" // Redis server address
	RedisPassword     = ""               // No password by default
	RedisDB           = 0                // Default DB

	JobStreamKey    = "conversion_jobs"     // Redis Stream key for new jobs
	JobStatusPrefix = "job:"              // Redis Hash key prefix for job status
	JobConsumerGroup = "conversion_group" // Redis Consumer Group name

	WorkerPoolSize    = 5                  // Number of concurrent conversions
	MaxJobRetries     = 3                  // Max retries for a failed conversion
	FileCleanupDelay  = 10 * time.Minute   // Time after completion to delete file
	PollingInterval   = 5 * time.Second    // How often workers check Redis Stream
)

// --- Global Instances ---
var (
	redisClient *redis.Client
	logger      *zap.Logger
)

// --- Main Server Setup ---

func main() {
	// Initialize context for graceful shutdown
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()

	// 1. Initialize Structured Logger (Zap)
	var err error
	logger, err = zap.NewProduction() // or zap.NewDevelopment() for development
	if err != nil {
		log.Fatalf("Failed to initialize logger: %v", err)
	}
	defer logger.Sync() // Flushes any buffered log entries

	logger.Info("Starting server...")

	// 2. Initialize Redis Client
	redisClient = redis.NewClient(&redis.Options{
		Addr:     RedisAddr,
		Password: RedisPassword,
		DB:       RedisDB,
	})

	// Test Redis connection
	if _, err := redisClient.Ping(ctx).Result(); err != nil {
		logger.Fatal("Could not connect to Redis", zap.Error(err))
	}
	logger.Info("Connected to Redis", zap.String("address", RedisAddr))

	// Ensure the Redis Stream consumer group exists
	_, err = redisClient.XGroupCreateMkStream(ctx, JobStreamKey, JobConsumerGroup, "0").Result()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		logger.Fatal("Failed to create Redis consumer group", zap.Error(err))
	}
	logger.Info("Redis consumer group ensured", zap.String("group", JobConsumerGroup))

	// 3. Start Worker Pool
	var wg sync.WaitGroup
	for i := 0; i < WorkerPoolSize; i++ {
		wg.Add(1)
		go startWorker(ctx, i, &wg)
	}

	// 4. Start File Cleanup Goroutine
	wg.Add(1)
	go startFileCleanup(ctx, &wg)

	// 5. Setup HTTP routes
	http.HandleFunc("/extract", handleExtract)
	http.HandleFunc("/status/", handleStatus)
	http.HandleFunc("/download/", handleDownload)

	server := &http.Server{
		Addr: ":8080",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			logger.Debug("Incoming HTTP request", zap.String("method", r.Method), zap.String("path", r.URL.Path))
			enableCORS(w)
			http.DefaultServeMux.ServeHTTP(w, r) // Use DefaultServeMux for routing
		}),
	}

	// Graceful shutdown for HTTP server
	go func() {
		<-ctx.Done() // Wait for main context to be cancelled
		logger.Info("Shutting down HTTP server...")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("HTTP server shutdown error", zap.Error(err))
		}
	}()

	logger.Info("🚀 Server running", zap.String("address", server.Addr))
	err = server.ListenAndServe()
	if err != http.ErrServerClosed {
		logger.Fatal("HTTP server failed to start or stopped unexpectedly", zap.Error(err))
	}

	// Wait for workers to finish
	wg.Wait()
	logger.Info("All workers and cleanup goroutine stopped.")
	logger.Info("Server gracefully shut down.")
}

// Enable CORS for browser requests
func enableCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

// --- Handlers ---

// handleExtract creates a new conversion job and adds it to the Redis Stream
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
		logger.Warn("Invalid JSON in extract request", zap.Error(err))
		return
	}

	if req.URL == "" {
		http.Error(w, "Missing YouTube URL", http.StatusBadRequest)
		logger.Warn("Missing YouTube URL in extract request")
		return
	}

	jobID := uuid.New().String()
	job := &ConversionJob{
		ID:         jobID,
		URL:        req.URL,
		Status:     StatusPending,
		CreatedAt:  time.Now(),
		MaxRetries: MaxJobRetries,
		WebhookURL: req.WebhookURL,
	}

	// Store job status in Redis Hash
	if err := saveJobToRedis(ctx, job); err != nil {
		http.Error(w, "Internal server error: Could not save job", http.StatusInternalServerError)
		logger.Error("Failed to save job to Redis", zap.String("job_id", jobID), zap.Error(err))
		return
	}

	// Add job to Redis Stream
	jobJSON, _ := json.Marshal(job) // Marshal job for stream
	_, err := redisClient.XAdd(ctx, &redis.XAddArgs{
		Stream: JobStreamKey,
		MaxLen: 1000, // Cap stream length to prevent excessive memory usage
		Approx: true,
		Values: map[string]interface{}{"job_id": jobID, "job_data": string(jobJSON)},
	}).Result()

	if err != nil {
		// If adding to stream fails, clean up job from Redis hash
		redisClient.Del(ctx, JobStatusPrefix+jobID)
		http.Error(w, "Internal server error: Could not queue job", http.StatusInternalServerError)
		logger.Error("Failed to add job to Redis Stream", zap.String("job_id", jobID), zap.Error(err))
		return
	}

	logger.Info("Job added to queue", zap.String("job_id", jobID), zap.String("url", req.URL))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"job_id":                jobID,
		"status":                string(job.Status),
		"check_status_endpoint": fmt.Sprintf("http://localhost:8080/status/%s", jobID),
	})
}

// handleStatus allows clients to check the progress of a conversion job from Redis
func handleStatus(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	jobID := filepath.Base(r.URL.Path)
	if jobID == "" {
		http.Error(w, "Missing job ID", http.StatusBadRequest)
		logger.Warn("Missing job ID in status request")
		return
	}

	job, err := getJobFromRedis(ctx, jobID)
	if err != nil {
		if err == redis.Nil {
			http.Error(w, "Job not found", http.StatusNotFound)
			logger.Info("Job not found in status request", zap.String("job_id", jobID))
		} else {
			http.Error(w, "Internal server error: Could not retrieve job status", http.StatusInternalServerError)
			logger.Error("Failed to get job from Redis", zap.String("job_id", jobID), zap.Error(err))
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(job)
}

// handleDownload serves the converted MP3 file
func handleDownload(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	filenameWithExt := filepath.Base(r.URL.Path)
	if !strings.HasSuffix(filenameWithExt, ".mp3") {
		http.Error(w, "Invalid file format", http.StatusBadRequest)
		logger.Warn("Invalid download request, missing .mp3 extension", zap.String("filename", filenameWithExt))
		return
	}
	jobID := strings.TrimSuffix(filenameWithExt, ".mp3")

	job, err := getJobFromRedis(ctx, jobID)
	if err != nil {
		if err == redis.Nil {
			http.Error(w, "File not found or job invalid", http.StatusNotFound)
		} else {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			logger.Error("Failed to get job from Redis for download", zap.String("job_id", jobID), zap.Error(err))
		}
		return
	}

	if job.Status != StatusCompleted || job.FilePath == "" {
		http.Error(w, "File not found or conversion not completed", http.StatusNotFound)
		logger.Info("Download request for uncompleted job", zap.String("job_id", jobID), zap.String("status", string(job.Status)))
		return
	}

	file, err := os.Open(job.FilePath)
	if err != nil {
		http.Error(w, "Error opening file", http.StatusInternalServerError)
		logger.Error("Error opening file for download", zap.String("file_path", job.FilePath), zap.Error(err))
		return
	}
	defer file.Close()

	logger.Info("File requested for download", zap.String("job_id", jobID), zap.String("file_path", job.FilePath))

	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filenameWithExt))
	io.Copy(w, file)
}

// --- Worker Logic ---

// startWorker listens on the Redis Stream and processes jobs
func startWorker(ctx context.Context, workerID int, wg *sync.WaitGroup) {
	defer wg.Done()
	logger.Info("Worker started", zap.Int("worker_id", workerID))

	// Create a unique consumer ID for this worker
	consumerID := fmt.Sprintf("consumer-%d-%s", workerID, uuid.New().String()[:8])

	for {
		select {
		case <-ctx.Done():
			logger.Info("Worker shutting down", zap.Int("worker_id", workerID))
			return
		default:
			// Read from stream using XReadGroup
			streams, err := redisClient.XReadGroup(ctx, &redis.XReadGroupArgs{
				Group:    JobConsumerGroup,
				Consumer: consumerID,
				Streams:  []string{JobStreamKey, ">"}, // Read new messages, starting from '>'
				Count:    1,                           // Read one message at a time
				Block:    PollingInterval,             // Block for 'PollingInterval' if no messages
				NoAck:    false,                       // We will manually ACK
			}).Result()

			if err == redis.Nil || (err != nil && strings.Contains(err.Error(), "timeout")) {
				// No new messages, continue polling
				continue
			} else if err != nil {
				logger.Error("Error reading from Redis Stream", zap.Int("worker_id", workerID), zap.Error(err))
				time.Sleep(PollingInterval) // Sleep before retrying to avoid tight loop
				continue
			}

			// Process the messages
			for _, stream := range streams {
				for _, msg := range stream.Messages {
					jobID := msg.Values["job_id"].(string)
					jobDataJSON := msg.Values["job_data"].(string)
					
					var job ConversionJob
					if err := json.Unmarshal([]byte(jobDataJSON), &job); err != nil {
						logger.Error("Failed to unmarshal job from stream", zap.String("stream_id", msg.ID), zap.Error(err))
						// ACK the message to remove it from the stream, as it's unprocessable
						redisClient.XAck(ctx, JobStreamKey, JobConsumerGroup, msg.ID)
						continue
					}

					// Fetch the latest job state from Redis (important for retries)
					latestJob, err := getJobFromRedis(ctx, jobID)
					if err != nil {
						logger.Error("Failed to get latest job state from Redis for processing",
							zap.String("job_id", jobID), zap.Error(err))
						redisClient.XAck(ctx, JobStreamKey, JobConsumerGroup, msg.ID) // ACK to prevent re-processing a non-existent job
						continue
					}
					// Use the latest state for processing
					job = *latestJob 

					// Process job
					processJob(ctx, &job, workerID)

					// Acknowledge the message after processing (or handling retries)
					if _, err := redisClient.XAck(ctx, JobStreamKey, JobConsumerGroup, msg.ID).Result(); err != nil {
						logger.Error("Failed to acknowledge message in Redis Stream",
							zap.String("job_id", job.ID), zap.String("stream_id", msg.ID), zap.Error(err))
					}
				}
			}
		}
	}
}

func processJob(ctx context.Context, job *ConversionJob, workerID int) {
	logger.Info("Processing job", zap.Int("worker_id", workerID), zap.String("job_id", job.ID), zap.String("url", job.URL))

	// Update job status
	job.Status = StatusProcessing
	job.StartedAt = time.Now()
	if err := saveJobToRedis(ctx, job); err != nil {
		logger.Error("Failed to update job status to processing in Redis", zap.String("job_id", job.ID), zap.Error(err))
		handleJobFailure(ctx, job, fmt.Sprintf("Failed to update status: %v", err))
		return
	}

	outputDir := "downloads"
	if err := os.MkdirAll(outputDir, os.ModePerm); err != nil {
		handleJobFailure(ctx, job, fmt.Sprintf("Error creating downloads directory: %v", err))
		return
	}
	outputPath := filepath.Join(outputDir, job.ID+".mp3")

	// --- Step 1: Extract direct audio stream URL via yt-dlp ---
	audioURL, meta, err := getAudioStreamFromYTDLP(ctx, job.URL)
	if err != nil {
		handleJobFailure(ctx, job, fmt.Sprintf("yt-dlp stream extraction failed: %v", err))
		return
	}
	job.Metadata = meta // Update metadata

	// --- Step 2: Convert stream to MP3 file using ffmpeg ---
	err = convertStreamToMP3(ctx, audioURL, outputPath)
	if err != nil {
		handleJobFailure(ctx, job, fmt.Sprintf("ffmpeg conversion failed: %v", err))
		return
	}

	// --- Job Completed Successfully ---
	job.Status = StatusCompleted
	job.CompletedAt = time.Now()
	job.FilePath = outputPath
	job.DownloadURL = fmt.Sprintf("http://localhost:8080/download/%s.mp3", job.ID)
	job.Error = "" // Clear any previous errors
	if err := saveJobToRedis(ctx, job); err != nil {
		logger.Error("Failed to update job status to completed in Redis", zap.String("job_id", job.ID), zap.Error(err))
		// Even if saving fails, the job completed. Log and continue.
	}

	logger.Info("Job completed successfully",
		zap.Int("worker_id", workerID),
		zap.String("job_id", job.ID),
		zap.String("download_url", job.DownloadURL),
		zap.Duration("duration", time.Since(job.StartedAt)),
	)

	// Send webhook callback if configured
	if job.WebhookURL != "" {
		sendWebhookCallback(ctx, job)
	}
}

// Helper to handle job failures with retries
func handleJobFailure(ctx context.Context, job *ConversionJob, errMsg string) {
	job.Retries++
	job.Error = errMsg

	if job.Retries <= job.MaxRetries {
		job.Status = StatusRetrying
		logger.Warn("Job failed, retrying...",
			zap.String("job_id", job.ID),
			zap.String("url", job.URL),
			zap.String("error", errMsg),
			zap.Int("retries", job.Retries),
			zap.Int("max_retries", job.MaxRetries),
		)
		// Re-add job to stream for retry after a delay
		// In a real-world scenario, you might add it to a separate "retry stream" or use Redis delayed queue.
		// For simplicity, we just put it back after updating status.
		time.Sleep(5 * time.Second) // Simulate a delay before retrying
		jobJSON, _ := json.Marshal(job)
		_, err := redisClient.XAdd(ctx, &redis.XAddArgs{
			Stream: JobStreamKey,
			Values: map[string]interface{}{"job_id": job.ID, "job_data": string(jobJSON)},
		}).Result()
		if err != nil {
			logger.Error("Failed to re-add job to Redis Stream for retry", zap.String("job_id", job.ID), zap.Error(err))
			// If cannot re-queue, mark as failed
			job.Status = StatusFailed
			job.Error = "Max retries exceeded and failed to re-queue: " + errMsg
			job.CompletedAt = time.Now()
		}
	} else {
		job.Status = StatusFailed
		job.CompletedAt = time.Now()
		logger.Error("Job failed permanently: Max retries exceeded",
			zap.String("job_id", job.ID),
			zap.String("url", job.URL),
			zap.String("error", errMsg),
			zap.Int("retries", job.Retries),
			zap.Int("max_retries", job.MaxRetries),
		)
	}

	if err := saveJobToRedis(ctx, job); err != nil {
		logger.Error("Failed to update job status in Redis after failure/retry", zap.String("job_id", job.ID), zap.Error(err))
	}

	// Send webhook callback if permanently failed and configured
	if job.Status == StatusFailed && job.WebhookURL != "" {
		sendWebhookCallback(ctx, job)
	}
}

// --- Redis Store Functions ---

func saveJobToRedis(ctx context.Context, job *ConversionJob) error {
	jobJSON, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("failed to marshal job: %w", err)
	}
	return redisClient.Set(ctx, JobStatusPrefix+job.ID, jobJSON, 0).Err() // No expiration
}

func getJobFromRedis(ctx context.Context, jobID string) (*ConversionJob, error) {
	jobJSON, err := redisClient.Get(ctx, JobStatusPrefix+jobID).Bytes()
	if err == redis.Nil {
		return nil, redis.Nil // Job not found
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get job from Redis: %w", err)
	}

	var job ConversionJob
	if err := json.Unmarshal(jobJSON, &job); err != nil {
		return nil, fmt.Errorf("failed to unmarshal job from Redis: %w", err)
	}
	return &job, nil
}

// --- yt-dlp and FFmpeg functions ---

// getAudioStreamFromYTDLP uses yt-dlp to get audio stream URL + metadata
func getAudioStreamFromYTDLP(ctx context.Context, videoURL string) (string, *Metadata, error) {
	cmd := exec.CommandContext(ctx, "./yt-dlp", "-f", "bestaudio", "--dump-json", "--no-warnings", videoURL)
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
		logger.Error("JSON parse error from yt-dlp output", zap.String("output", out.String()), zap.Error(err))
		return "", nil, fmt.Errorf("JSON parse error from yt-dlp output: %v", err)
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

// convertStreamToMP3 converts audio stream URL to MP3 with FFmpeg optimizations
func convertStreamToMP3(ctx context.Context, audioURL, outputPath string) error {
	start := time.Now()

	cmd := exec.CommandContext(ctx, "./ffmpeg",
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
		logger.Error("FFmpeg command failed", zap.String("audio_url", audioURL),
			zap.String("output_path", outputPath), zap.String("stderr", out.String()), zap.Error(err))
		return fmt.Errorf("ffmpeg error: %v\nOutput: %s", err, out.String())
	}

	elapsed := time.Since(start)
	logger.Info("FFmpeg conversion to MP3 completed",
		zap.String("output_path", outputPath),
		zap.Duration("time_taken", elapsed),
	)
	return nil
}

// --- File Cleanup Logic ---

func startFileCleanup(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	logger.Info("File cleanup goroutine started")

	ticker := time.NewTicker(FileCleanupDelay / 2) // Check more frequently than cleanup delay
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("File cleanup goroutine shutting down")
			return
		case <-ticker.C:
			logger.Debug("Running file cleanup check...")
			cleanFiles(ctx)
		}
	}
}

func cleanFiles(ctx context.Context) {
	iter := redisClient.Scan(ctx, 0, JobStatusPrefix+"*", 0).Iterator()
	for iter.Next(ctx) {
		key := iter.Val()
		jobID := strings.TrimPrefix(key, JobStatusPrefix)

		job, err := getJobFromRedis(ctx, jobID)
		if err == redis.Nil {
			// Job already deleted or somehow invalid, delete key from Redis
			redisClient.Del(ctx, key)
			continue
		}
		if err != nil {
			logger.Error("Error getting job from Redis for cleanup", zap.String("job_id", jobID), zap.Error(err))
			continue
		}

		// Only clean up completed jobs with a file path
		if job.Status == StatusCompleted && job.FilePath != "" && !job.CompletedAt.IsZero() {
			if time.Since(job.CompletedAt) > FileCleanupDelay {
				// Delete the actual file
				if err := os.Remove(job.FilePath); err != nil {
					if !os.IsNotExist(err) { // Log if error is not "file not found"
						logger.Error("Failed to delete file during cleanup",
							zap.String("job_id", job.ID),
							zap.String("file_path", job.FilePath),
							zap.Error(err),
						)
					}
				} else {
					logger.Info("Successfully deleted old file",
						zap.String("job_id", job.ID),
						zap.String("file_path", job.FilePath),
					)
				}

				// Remove job data from Redis as well
				if err := redisClient.Del(ctx, key).Err(); err != nil {
					logger.Error("Failed to delete job from Redis after file cleanup", zap.String("job_id", job.ID), zap.Error(err))
				}
			}
		}
	}
	if err := iter.Err(); err != nil {
		logger.Error("Error iterating Redis keys during cleanup", zap.Error(err))
	}
}

// --- Webhook Callback Logic ---

func sendWebhookCallback(ctx context.Context, job *ConversionJob) {
	if job.WebhookURL == "" {
		return
	}

	payload, err := json.Marshal(job)
	if err != nil {
		logger.Error("Failed to marshal job for webhook payload", zap.String("job_id", job.ID), zap.Error(err))
		return
	}

	req, err := http.NewRequestWithContext(ctx, "POST", job.WebhookURL, bytes.NewBuffer(payload))
	if err != nil {
		logger.Error("Failed to create webhook request", zap.String("job_id", job.ID), zap.Error(err))
		return
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second} // Short timeout for webhooks
	resp, err := client.Do(req)
	if err != nil {
		logger.Error("Failed to send webhook callback",
			zap.String("job_id", job.ID),
			zap.String("webhook_url", job.WebhookURL),
			zap.Error(err),
		)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		responseBody, _ := io.ReadAll(resp.Body)
		logger.Error("Webhook callback received non-success status",
			zap.String("job_id", job.ID),
			zap.String("webhook_url", job.WebhookURL),
			zap.Int("status_code", resp.StatusCode),
			zap.String("response_body", string(responseBody)),
		)
	} else {
		logger.Info("Webhook callback sent successfully",
			zap.String("job_id", job.ID),
			zap.String("webhook_url", job.WebhookURL),
			zap.Int("status_code", resp.StatusCode),
		)
	}
}
