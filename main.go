package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
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
	ID           string
	URL          string
	Status       JobStatus
	CreatedAt    time.Time
	StartedAt    time.Time
	CompletedAt  time.Time
	FilePath     string    // Local path to the converted MP3
	DownloadURL  string    // Public URL for download
	Error        string    // Error message if conversion failed
	Metadata     *Metadata // Extracted metadata
	Retries      int       // Number of retries attempted
	MaxRetries   int       // Maximum allowed retries
}

// JobQueue is a channel to send jobs to workers
var jobQueue chan *ConversionJob

// JobStore to keep track of all jobs by ID
var jobStore = struct {
	sync.RWMutex
	jobs map[string]*ConversionJob
}{
	jobs: make(map[string]*ConversionJob),
}

// --- Configuration ---
const (
	WorkerPoolSize = 5     // Number of concurrent conversions
	MaxJobRetries  = 3     // Max retries for a failed conversion
	JobQueueCapacity = 100 // Max pending jobs in queue
)

// --- Main Server Setup ---

func main() {
	// Initialize job queue
	jobQueue = make(chan *ConversionJob, JobQueueCapacity)

	// Start worker pool
	for i := 0; i < WorkerPoolSize; i++ {
		go startWorker(i)
	}

	// Setup HTTP routes
	http.HandleFunc("/extract", handleExtract)
	http.HandleFunc("/status/", handleStatus) // New endpoint to check job status
	http.HandleFunc("/download/", handleDownload)

	fmt.Printf("🚀 Server running on http://localhost:8080 with %d workers\n", WorkerPoolSize)
	log.Fatal(http.ListenAndServe(":8080", nil))
}

// Enable CORS for browser requests
func enableCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

// --- Handlers ---

// handleExtract creates a new conversion job and adds it to the queue
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

	jobID := uuid.New().String()
	job := &ConversionJob{
		ID:         jobID,
		URL:        req.URL,
		Status:     StatusPending,
		CreatedAt:  time.Now(),
		MaxRetries: MaxJobRetries,
	}

	jobStore.Lock()
	jobStore.jobs[jobID] = job
	jobStore.Unlock()

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
		delete(jobStore.jobs, jobID) // Remove job if it couldn't be queued
		jobStore.Unlock()
		http.Error(w, "Server busy, please try again later.", http.StatusServiceUnavailable)
		fmt.Printf("❌ Job %s for URL %s rejected, queue full.\n", jobID, req.URL)
	}
}

// handleStatus allows clients to check the progress of a conversion job
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
		Progress     string    `json:"progress,omitempty"` // Could add percentage here
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

// handleDownload serves the converted MP3 file
func handleDownload(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	// The token is actually the jobID.mp3
	filenameWithExt := filepath.Base(r.URL.Path)
	jobID := filenameWithExt[:len(filenameWithExt)-len(".mp3")] // Extract jobID from filename

	jobStore.RLock()
	job, exists := jobStore.jobs[jobID]
	jobStore.RUnlock()

	if !exists || job.Status != StatusCompleted {
		http.Error(w, "File not found or conversion not completed", http.StatusNotFound)
		return
	}
	if job.FilePath == "" { // Should not happen if status is completed, but as a safeguard
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
	io.Copy(w, file)
}

// --- Worker Logic ---

// startWorker listens on the jobQueue and processes jobs
func startWorker(workerID int) {
	fmt.Printf("Worker %d started.\n", workerID)
	for job := range jobQueue {
		processJob(job, workerID)
	}
}

func processJob(job *ConversionJob, workerID int) {
	log.Printf("Worker %d: Processing job %s for URL: %s\n", workerID, job.ID, job.URL)

	// Update job status
	jobStore.Lock()
	job.Status = StatusProcessing
	job.StartedAt = time.Now()
	jobStore.Unlock()

	outputDir := "downloads"
	if err := os.MkdirAll(outputDir, os.ModePerm); err != nil {
		updateJobStatus(job, StatusFailed, fmt.Sprintf("Error creating downloads directory: %v", err))
		return
	}
	outputPath := filepath.Join(outputDir, job.ID+".mp3")

	// --- Step 1: Extract direct audio stream URL via yt-dlp ---
	audioURL, meta, err := getAudioStreamFromYTDLP(job.URL) // Renamed to avoid conflict
	if err != nil {
		handleJobFailure(job, err, "yt-dlp stream extraction failed")
		return
	}

	// --- Step 2: Convert stream to MP3 file using ffmpeg ---
	err = convertStreamToMP3(audioURL, outputPath)
	if err != nil {
		handleJobFailure(job, err, "ffmpeg conversion failed")
		return
	}

	// --- Job Completed Successfully ---
	jobStore.Lock()
	job.Status = StatusCompleted
	job.CompletedAt = time.Now()
	job.FilePath = outputPath
	job.DownloadURL = fmt.Sprintf("http://localhost:8080/download/%s.mp3", job.ID)
	job.Metadata = meta
	job.Error = "" // Clear any previous errors
	jobStore.Unlock()

	log.Printf("Worker %d: Job %s completed successfully. Download: %s\n", workerID, job.ID, job.DownloadURL)
}

// Helper to update job status
func updateJobStatus(job *ConversionJob, status JobStatus, errMsg string) {
	jobStore.Lock()
	job.Status = status
	job.Error = errMsg
	if status == StatusFailed {
		job.CompletedAt = time.Now() // Mark as completed (failed)
	}
	jobStore.Unlock()
	log.Printf("Job %s status updated to %s: %s\n", job.ID, status, errMsg)
}

// Helper to handle job failures with retries
func handleJobFailure(job *ConversionJob, err error, stage string) {
	job.Retries++
	if job.Retries <= job.MaxRetries {
		log.Printf("Job %s (%s): %s. Retrying (%d/%d)...\n", job.ID, job.URL, err.Error(), job.Retries, job.MaxRetries)
		// Re-add to queue for retry (with a small delay in a real-world scenario)
		time.Sleep(5 * time.Second) // Simulate a delay before retrying
		select {
		case jobQueue <- job:
			// Job successfully re-queued for retry. Status remains processing or pending depending on implementation
			// For simplicity here, it just goes back to queue, actual status might be 'retrying'
		default:
			// If queue is full even for retries, mark as failed
			updateJobStatus(job, StatusFailed, fmt.Sprintf("%s: %v. Max retries exceeded and queue full.", stage, err))
		}
	} else {
		updateJobStatus(job, StatusFailed, fmt.Sprintf("%s: %v. Max retries (%d) exceeded.", stage, err, job.MaxRetries))
	}
}

// --- yt-dlp and FFmpeg functions (reused from previous optimized version) ---

// getAudioStreamFromYTDLP uses yt-dlp to get audio stream URL + metadata
func getAudioStreamFromYTDLP(videoURL string) (string, *Metadata, error) {
	cmd := exec.Command("./yt-dlp", "-f", "bestaudio", "--dump-json", "--no-warnings", videoURL)
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
		URL      string  `json:"url"` // The URL of the selected 'bestaudio' stream
		Ext      string  `json:"ext"`
		Abr      float64 `json:"abr"` // Use float64 for parsing, then convert to int
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

// convertStreamToMP3 converts audio stream URL to MP3 with FFmpeg optimizations
func convertStreamToMP3(audioURL, outputPath string) error {
	start := time.Now()

	cmd := exec.Command("./ffmpeg",
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
