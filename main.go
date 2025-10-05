package main

import (
    "context"
    "encoding/json"
    "fmt"
    "io"
    "log"
    "net"
    "net/http"
    "net/url"
    "os"
    "os/exec"
    "path/filepath"
    "runtime"
    "strconv"
    "strings"
    "sync"
    "time"

    "github.com/google/uuid"
    "golang.org/x/time/rate"
)

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
    URL          string `json:"url"`
    CaptchaToken string `json:"captcha_token"`
}

// Response structure
type Response struct {
	DownloadEndpoint string    `json:"download_endpoint"`
	Metadata         *Metadata `json:"metadata"`
	Token            string    `json:"token"`
}

// Status/Extract response structure for async flow
type JobResponse struct {
    Status          string     `json:"status"`
    Token           string     `json:"token"`
    StatusEndpoint  string     `json:"status_endpoint"`
    DownloadEndpoint string    `json:"download_endpoint,omitempty"`
    Metadata        *Metadata  `json:"metadata,omitempty"`
    Message         string     `json:"message,omitempty"`
}

// Global configuration populated from environment variables
var (
    ytDlpPath       = getEnv("YTDLP_BIN", "./yt-dlp")
    ffmpegPath      = getEnv("FFMPEG_BIN", "./ffmpeg")
    downloadsDir    = getEnv("DOWNLOADS_DIR", "downloads")
    baseURLOverride = os.Getenv("BASE_URL") // optional, e.g., https://api.example.com
    turnstileSecret = os.Getenv("TURNSTILE_SECRET")
    turnstileTestMode = getEnvBool("TURNSTILE_TEST_MODE", false)

    ytdlpTimeout  = getEnvDuration("YTDLP_TIMEOUT", 60*time.Second)
    ffmpegTimeout = getEnvDuration("FFMPEG_TIMEOUT", 8*time.Minute)
    fileTTL        = getEnvDuration("FILE_TTL", 15*time.Minute)
    mp3Bitrate     = getEnv("MP3_BITRATE", "128k")

    // worker pool / queue
    workerCount  = getEnvInt("WORKER_COUNT", max(2, runtime.NumCPU()))
    queueSize    = getEnvInt("QUEUE_SIZE", 2000)
    jobQueue     chan string

    // jobs and caching
    jobMu         sync.RWMutex
    tokenToJob    = map[string]*Job{}
    videoIDToToken = map[string]string{}

    // per-IP rate limiters
    rateRPS   = getEnvFloat("RATE_RPS", 5)
    rateBurst = getEnvInt("RATE_BURST", 10)
    clientsMu sync.Mutex
    clients   = map[string]*clientLimiter{}
)

func main() {
    // Configure logger with timestamps
    log.SetFlags(log.LstdFlags | log.Lmicroseconds)

    preflight()

    // Routes
    http.HandleFunc("/extract", handleExtract)
    http.HandleFunc("/status/", handleStatus)
    http.HandleFunc("/download/", handleDownload)
    // Serve static test page on /
    http.Handle("/", http.FileServer(http.Dir("static")))

    // Initialize queue and workers
    jobQueue = make(chan string, queueSize)
    for i := 0; i < workerCount; i++ {
        go worker(i, jobQueue)
    }
    go janitor()

    port := getEnv("PORT", "8080")
    log.Printf("🚀 Server running on http://localhost:%s (workers=%d, queue=%d)\n", port, workerCount, queueSize)
    log.Fatal(http.ListenAndServe(":"+port, nil))
}

// Enable CORS for browser requests
func enableCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

// Handle extract request
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

    // IP rate limiting
    if !allow(r) {
        http.Error(w, "Too many requests", http.StatusTooManyRequests)
        return
    }

    var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

    videoURL := req.URL
	if videoURL == "" {
		http.Error(w, "Missing YouTube URL", http.StatusBadRequest)
		return
	}

    if req.CaptchaToken == "" {
        if !turnstileTestMode {
            http.Error(w, "Missing captcha_token", http.StatusBadRequest)
            return
        }
        log.Printf("⚠️  TURNSTILE_TEST_MODE enabled: proceeding without captcha_token")
    }

    // 🟢 Log: URL received
    log.Printf("\n🎬 Received URL: %s\n", videoURL)

    // Verify Cloudflare Turnstile before enqueuing (unless test mode)
    if !turnstileTestMode {
        clientIP := getClientIP(r)
        log.Printf("🛡️ Verifying Turnstile token for IP=%s...", clientIP)
        ok, err := verifyTurnstile(r.Context(), req.CaptchaToken, clientIP)
        if err != nil {
            log.Printf("❌ Turnstile verification error: %v", err)
            http.Error(w, "Captcha verification error", http.StatusInternalServerError)
            return
        }
        if !ok {
            log.Printf("❌ Turnstile verification failed")
            http.Error(w, "Captcha verification failed", http.StatusBadRequest)
            return
        }
        log.Printf("✅ Turnstile verified")
    } else {
        log.Printf("🧪 TURNSTILE_TEST_MODE enabled: skipping Turnstile verification")
    }

    // Caching/dedup by video ID
    videoID := extractYouTubeID(videoURL)

    jobMu.Lock()
    if existingToken, ok := videoIDToToken[videoID]; ok {
        if job, ok2 := tokenToJob[existingToken]; ok2 {
            // Return current state of existing job
            resp := buildJobResponse(r, job)
            jobMu.Unlock()
            w.Header().Set("Content-Type", "application/json")
            if job.Status == StatusDone {
                w.WriteHeader(http.StatusOK)
            } else {
                w.WriteHeader(http.StatusAccepted)
            }
            json.NewEncoder(w).Encode(resp)
            return
        }
        // mapping stale, remove
        delete(videoIDToToken, videoID)
    }

    // Create new job
    token := uuid.New().String() + ".mp3"
    outputPath := filepath.Join(downloadsDir, token)
    job := &Job{
        Token:      token,
        VideoURL:   videoURL,
        VideoID:    videoID,
        OutputPath: outputPath,
        Status:     StatusQueued,
        CreatedAt:  time.Now(),
        UpdatedAt:  time.Now(),
    }
    tokenToJob[token] = job
    videoIDToToken[videoID] = token
    jobMu.Unlock()

    // enqueue non-blocking
    select {
    case jobQueue <- token:
        log.Printf("📥 Job queued token=%s videoID=%s", token, videoID)
    default:
        log.Printf("🚫 Queue full; rejecting job")
        http.Error(w, "Queue is busy. Try again later.", http.StatusServiceUnavailable)
        return
    }

    // Respond with status endpoint
    resp := JobResponse{
        Status:         string(StatusQueued),
        Token:          token,
        StatusEndpoint: fmt.Sprintf("%s/status/%s", inferBaseURL(r), token),
    }
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusAccepted)
    json.NewEncoder(w).Encode(resp)
}

// Use yt-dlp to get audio stream URL + metadata
func getAudioStream(parentCtx context.Context, videoURL string) (string, *Metadata, error) {
    ctx, cancel := context.WithTimeout(parentCtx, ytdlpTimeout)
    defer cancel()
    args := []string{"-f", "bestaudio", "--dump-single-json", "--no-warnings", "--no-call-home", "--geo-bypass", "--ignore-config", videoURL}
    cmd := exec.CommandContext(ctx, ytDlpPath, args...)
    if wd, err := os.Getwd(); err == nil { cmd.Dir = wd }
    log.Printf("▶️ Running yt-dlp: %s %s", ytDlpPath, strings.Join(args, " "))
    log.Printf("   cwd=%s PATH=%s", cmd.Dir, os.Getenv("PATH"))
    out, err := cmd.CombinedOutput()
    if err != nil {
        return "", nil, fmt.Errorf("yt-dlp failed: %v\nCommand: %s %s\nOutput:\n%s", err, ytDlpPath, strings.Join(args, " "), string(out))
    }

	var data struct {
		Title    string  `json:"title"`
		Uploader string  `json:"uploader"`
		Duration float64 `json:"duration"`
		URL      string  `json:"url"`
		Ext      string  `json:"ext"`
		Abr      int     `json:"abr"`
	}

    if err := json.Unmarshal(out, &data); err != nil {
        return "", nil, fmt.Errorf("JSON parse error: %v\nOutput: %s", err, string(out))
	}

	meta := &Metadata{
		Title:    data.Title,
		Uploader: data.Uploader,
		Duration: data.Duration,
		AudioURL: data.URL,
		Ext:      data.Ext,
		Abr:      data.Abr,
	}

    log.Printf("✅ yt-dlp OK: title=%q uploader=%q duration=%.0fs", meta.Title, meta.Uploader, meta.Duration)
    return data.URL, meta, nil
}

// Convert audio stream URL to MP3 at a target path
func convertToMP3(parentCtx context.Context, audioURL string, outputPath string) error {
    if err := os.MkdirAll(filepath.Dir(outputPath), os.ModePerm); err != nil {
        return err
    }

    start := time.Now()

    ctx, cancel := context.WithTimeout(parentCtx, ffmpegTimeout)
    defer cancel()
    // Use libmp3lame, lower bitrate to reduce CPU, hide banner for speed
    args := []string{
        "-nostdin", "-hide_banner", "-loglevel", "error",
        "-y", "-i", audioURL, "-vn",
        "-b:a", mp3Bitrate, "-ar", "44100",
        "-f", "mp3", outputPath,
    }
    cmd := exec.CommandContext(ctx, ffmpegPath,
        args...,
    )
    if wd, err := os.Getwd(); err == nil { cmd.Dir = wd }
    log.Printf("▶️ Running ffmpeg: %s %s", ffmpegPath, strings.Join(args, " "))
    log.Printf("   cwd=%s PATH=%s", cmd.Dir, os.Getenv("PATH"))
    out, err := cmd.CombinedOutput()
    if err != nil {
        return fmt.Errorf("ffmpeg error: %v\nCommand: %s %s\nOutput:\n%s", err, ffmpegPath, strings.Join(args, " "), string(out))
    }

    elapsed := time.Since(start)
    log.Printf("⏱️ Conversion time: %.2fs", elapsed.Seconds())
    if fi, err := os.Stat(outputPath); err == nil {
        log.Printf("💾 Saved: %s (%d bytes)", outputPath, fi.Size())
    } else {
        log.Printf("⚠️ Expected output missing: %s (err=%v)", outputPath, err)
    }
    return nil
}

// Preflight checks for environment and permissions
func preflight() {
    wd, _ := os.Getwd()
    absYt, _ := filepath.Abs(ytDlpPath)
    absFf, _ := filepath.Abs(ffmpegPath)
    log.Printf("🔧 Preflight: wd=%s", wd)
    log.Printf("🔧 Binaries: yt-dlp=%s ffmpeg=%s", absYt, absFf)
    if err := os.MkdirAll(downloadsDir, os.ModePerm); err != nil {
        log.Printf("❌ Cannot create downloads dir %s: %v", downloadsDir, err)
    } else {
        // write test file to confirm permissions
        testFile := filepath.Join(downloadsDir, ".perm_test")
        _ = os.WriteFile(testFile, []byte("ok"), 0644)
        if fi, err := os.Stat(testFile); err == nil {
            log.Printf("🔐 Downloads writable: %s (%d bytes)", testFile, fi.Size())
            _ = os.Remove(testFile)
        } else {
            log.Printf("❌ Downloads not writable: %v", err)
        }
    }
}

// Serve MP3 file download
func handleDownload(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

    token := filepath.Base(r.URL.Path)
    filePath := filepath.Join(downloadsDir, token)

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}

	file, err := os.Open(filePath)
	if err != nil {
		http.Error(w, "Error opening file", http.StatusInternalServerError)
		return
	}
	defer file.Close()

    log.Printf("⬇️  File requested for download: %s", token)

	w.Header().Set("Content-Type", "audio/mpeg")
    w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", token))
	io.Copy(w, file)
}

// handleStatus returns job state for a token
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
    if !allow(r) {
        http.Error(w, "Too many requests", http.StatusTooManyRequests)
        return
    }

    token := filepath.Base(r.URL.Path)
    jobMu.RLock()
    job, ok := tokenToJob[token]
    jobMu.RUnlock()
    if !ok {
        http.Error(w, "Unknown token", http.StatusNotFound)
        return
    }
    resp := buildJobResponse(r, job)
    w.Header().Set("Content-Type", "application/json")
    if job.Status == StatusDone {
        w.WriteHeader(http.StatusOK)
    } else if job.Status == StatusError {
        w.WriteHeader(http.StatusInternalServerError)
    } else {
        w.WriteHeader(http.StatusAccepted)
    }
    json.NewEncoder(w).Encode(resp)
}

// Job and worker infrastructure
type JobStatus string

const (
    StatusQueued     JobStatus = "queued"
    StatusProcessing JobStatus = "processing"
    StatusDone       JobStatus = "done"
    StatusError      JobStatus = "error"
)

type Job struct {
    Token      string
    VideoURL   string
    VideoID    string
    OutputPath string
    Status     JobStatus
    Metadata   *Metadata
    Message    string
    CreatedAt  time.Time
    UpdatedAt  time.Time
    ExpiresAt  time.Time
}

func worker(id int, queue <-chan string) {
    log.Printf("👷 worker-%d started", id)
    for token := range queue {
        jobMu.RLock()
        job := tokenToJob[token]
        jobMu.RUnlock()
        if job == nil {
            continue
        }

        // start processing
        jobMu.Lock()
        job.Status = StatusProcessing
        job.UpdatedAt = time.Now()
        jobMu.Unlock()
        log.Printf("👷 worker-%d processing token=%s", id, token)

        // perform extraction and conversion
        ctx := context.Background()
        audioURL, meta, err := getAudioStream(ctx, job.VideoURL)
        if err == nil {
            err = convertToMP3(ctx, audioURL, job.OutputPath)
        }

        jobMu.Lock()
        if err != nil {
            job.Status = StatusError
            job.Message = err.Error()
            job.UpdatedAt = time.Now()
            log.Printf("❌ job token=%s error=%v", token, err)
        } else {
            job.Status = StatusDone
            job.Metadata = meta
            job.Metadata.AudioURL = audioURL
            job.UpdatedAt = time.Now()
            job.ExpiresAt = time.Now().Add(fileTTL)
            log.Printf("✅ job token=%s done; file=%s", token, job.OutputPath)
        }
        jobMu.Unlock()
    }
}

func buildJobResponse(r *http.Request, job *Job) JobResponse {
    resp := JobResponse{
        Status:         string(job.Status),
        Token:          job.Token,
        StatusEndpoint: fmt.Sprintf("%s/status/%s", inferBaseURL(r), job.Token),
        Message:        job.Message,
    }
    if job.Status == StatusDone {
        resp.DownloadEndpoint = fmt.Sprintf("%s/download/%s", inferBaseURL(r), job.Token)
        resp.Metadata = job.Metadata
    }
    return resp
}

// Janitor periodically removes expired files and stale jobs
func janitor() {
    ticker := time.NewTicker(1 * time.Minute)
    defer ticker.Stop()
    for range ticker.C {
        // cleanup files
        entries, err := os.ReadDir(downloadsDir)
        if err == nil {
            cutoff := time.Now().Add(-fileTTL)
            for _, e := range entries {
                if e.IsDir() { continue }
                name := e.Name()
                if !strings.HasSuffix(strings.ToLower(name), ".mp3") { continue }
                info, err := e.Info()
                if err != nil { continue }
                if info.ModTime().Before(cutoff) {
                    fp := filepath.Join(downloadsDir, name)
                    _ = os.Remove(fp)
                }
            }
        }

        // cleanup jobs and limiter map
        jobMu.Lock()
        for token, job := range tokenToJob {
            if job.Status == StatusDone && time.Since(job.UpdatedAt) > fileTTL {
                delete(tokenToJob, token)
                if job.VideoID != "" {
                    if cur, ok := videoIDToToken[job.VideoID]; ok && cur == token {
                        delete(videoIDToToken, job.VideoID)
                    }
                }
            }
            if job.Status == StatusError && time.Since(job.UpdatedAt) > 10*time.Minute {
                delete(tokenToJob, token)
            }
        }
        jobMu.Unlock()

        // prune old clients
        clientsMu.Lock()
        for ip, c := range clients {
            if time.Since(c.lastSeen) > 10*time.Minute {
                delete(clients, ip)
            }
        }
        clientsMu.Unlock()
    }
}

// Rate limiting per client IP
type clientLimiter struct {
    limiter  *rate.Limiter
    lastSeen time.Time
}

func allow(r *http.Request) bool {
    ip := getClientIP(r)
    now := time.Now()
    clientsMu.Lock()
    cl, exists := clients[ip]
    if !exists {
        cl = &clientLimiter{limiter: rate.NewLimiter(rate.Limit(rateRPS), rateBurst)}
        clients[ip] = cl
    }
    cl.lastSeen = now
    clientsMu.Unlock()
    return cl.limiter.Allow()
}

// Utility helpers
func max(a, b int) int { if a > b { return a } ; return b }

func getEnvFloat(key string, def float64) float64 {
    if v := os.Getenv(key); v != "" {
        if f, err := strconv.ParseFloat(v, 64); err == nil {
            return f
        }
    }
    return def
}

// extractYouTubeID attempts to derive a stable video ID from common URL forms
func extractYouTubeID(raw string) string {
    u, err := url.Parse(raw)
    if err != nil { return raw }
    host := strings.ToLower(u.Host)
    if strings.Contains(host, "youtu.be") {
        return strings.TrimPrefix(u.Path, "/")
    }
    if strings.Contains(host, "youtube.com") {
        q := u.Query().Get("v")
        if q != "" { return q }
        // Short forms like /shorts/<id>
        parts := strings.Split(strings.Trim(u.Path, "/"), "/")
        if len(parts) >= 2 && (parts[0] == "shorts" || parts[0] == "embed") {
            return parts[1]
        }
    }
    return raw
}

// verifyTurnstile validates a Cloudflare Turnstile token using the secret from env
func verifyTurnstile(ctx context.Context, token string, remoteIP string) (bool, error) {
    if turnstileSecret == "" {
        // If not configured, reject to enforce security; change to true to disable for local testing
        return false, fmt.Errorf("TURNSTILE_SECRET not configured")
    }

    form := url.Values{}
    form.Set("secret", turnstileSecret)
    form.Set("response", token)
    if remoteIP != "" {
        form.Set("remoteip", remoteIP)
    }

    req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://challenges.cloudflare.com/turnstile/v0/siteverify", strings.NewReader(form.Encode()))
    if err != nil {
        return false, err
    }
    req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

    httpClient := &http.Client{Timeout: 10 * time.Second}
    resp, err := httpClient.Do(req)
    if err != nil {
        return false, err
    }
    defer resp.Body.Close()

    var body struct {
        Success     bool     `json:"success"`
        ErrorCodes  []string `json:"error-codes"`
        ChallengeTS string   `json:"challenge_ts"`
        Hostname    string   `json:"hostname"`
        Action      string   `json:"action"`
        Cdata       string   `json:"cdata"`
    }

    if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
        return false, err
    }

    if !body.Success {
        return false, nil
    }
    return true, nil
}

// inferBaseURL builds the public base URL from request or override
func inferBaseURL(r *http.Request) string {
    if baseURLOverride != "" {
        return strings.TrimRight(baseURLOverride, "/")
    }
    scheme := r.Header.Get("X-Forwarded-Proto")
    if scheme == "" {
        scheme = "http"
        if r.TLS != nil {
            scheme = "https"
        }
    }
    host := r.Host
    return fmt.Sprintf("%s://%s", scheme, host)
}

// getClientIP attempts to extract the real client IP from headers or remote addr
func getClientIP(r *http.Request) string {
    xff := r.Header.Get("X-Forwarded-For")
    if xff != "" {
        parts := strings.Split(xff, ",")
        if len(parts) > 0 {
            return strings.TrimSpace(parts[0])
        }
    }
    xri := r.Header.Get("X-Real-IP")
    if xri != "" {
        return xri
    }
    host, _, err := net.SplitHostPort(r.RemoteAddr)
    if err == nil {
        return host
    }
    return r.RemoteAddr
}

// Helpers to read environment variables
func getEnv(key, def string) string {
    if v := os.Getenv(key); v != "" {
        return v
    }
    return def
}

func getEnvInt(key string, def int) int {
    if v := os.Getenv(key); v != "" {
        if n, err := strconv.Atoi(v); err == nil {
            return n
        }
    }
    return def
}

func getEnvDuration(key string, def time.Duration) time.Duration {
    if v := os.Getenv(key); v != "" {
        if d, err := time.ParseDuration(v); err == nil {
            return d
        }
    }
    return def
}

func getEnvBool(key string, def bool) bool {
    if v := os.Getenv(key); v != "" {
        if b, err := strconv.ParseBool(v); err == nil {
            return b
        }
        switch strings.ToLower(v) {
        case "1", "true", "yes", "on", "y":
            return true
        case "0", "false", "no", "off", "n":
            return false
        }
    }
    return def
}
