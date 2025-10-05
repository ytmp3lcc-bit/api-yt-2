package main

import (
    "bytes"
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
    "strconv"
    "strings"
    "time"

    "github.com/google/uuid"
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

    // semaphore to limit concurrent conversions
    sem chan struct{}
)

func main() {
    // Configure logger with timestamps
    log.SetFlags(log.LstdFlags | log.Lmicroseconds)

    // Initialize concurrency limiter
    maxConc := getEnvInt("MAX_CONCURRENCY", 2)
    if maxConc < 1 {
        maxConc = 1
    }
    sem = make(chan struct{}, maxConc)

    // Routes
    http.HandleFunc("/extract", handleExtract)
    http.HandleFunc("/download/", handleDownload)
    // Serve static test page on /
    http.Handle("/", http.FileServer(http.Dir("static")))

    port := getEnv("PORT", "8080")
    log.Printf("🚀 Server running on http://localhost:%s (concurrency=%d)\n", port, maxConc)
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

    // Concurrency limiting: fail fast if saturated
    select {
    case sem <- struct{}{}:
        defer func() { <-sem }()
    default:
        http.Error(w, "Server busy, try again later", http.StatusTooManyRequests)
        return
    }

    // Verify Cloudflare Turnstile before processing (unless test mode)
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

    // 1️⃣ Extract direct audio stream URL via yt-dlp
    log.Printf("🔍 Step 1: Extracting audio stream…")
    audioURL, meta, err := getAudioStream(r.Context(), videoURL)
	if err != nil {
        log.Printf("❌ yt-dlp error: %v", err)
		http.Error(w, fmt.Sprintf("yt-dlp error: %v", err), http.StatusInternalServerError)
		return
	}
    log.Printf("✅ Audio stream extracted successfully")

	// 2️⃣ Convert stream to MP3 file using ffmpeg
    log.Printf("🎧 Step 2: Converting to MP3…")
    filePath, err := convertToMP3(r.Context(), audioURL)
	if err != nil {
        log.Printf("❌ ffmpeg error: %v", err)
		http.Error(w, fmt.Sprintf("ffmpeg error: %v", err), http.StatusInternalServerError)
		return
	}
    log.Printf("💾 Saving file… %s", filePath)
    log.Printf("✅ Done ✅")

    // Build response

	token := filepath.Base(filePath)
	meta.AudioURL = audioURL

    response := Response{
        DownloadEndpoint: fmt.Sprintf("%s/download/%s", inferBaseURL(r), token),
        Metadata:         meta,
        Token:            token,
    }

    log.Printf("📦 Step 3: File ready for download at: %s", response.DownloadEndpoint)
    log.Printf("------------------------------------------------------------")

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// Use yt-dlp to get audio stream URL + metadata
func getAudioStream(parentCtx context.Context, videoURL string) (string, *Metadata, error) {
    ctx, cancel := context.WithTimeout(parentCtx, ytdlpTimeout)
    defer cancel()
    cmd := exec.CommandContext(ctx, ytDlpPath, "-f", "bestaudio", "--dump-single-json", "--no-warnings", videoURL)
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
		Abr      int     `json:"abr"`
	}

	if err := json.Unmarshal(out.Bytes(), &data); err != nil {
		return "", nil, fmt.Errorf("JSON parse error: %v\nOutput: %s", err, out.String())
	}

	meta := &Metadata{
		Title:    data.Title,
		Uploader: data.Uploader,
		Duration: data.Duration,
		AudioURL: data.URL,
		Ext:      data.Ext,
		Abr:      data.Abr,
	}

    return data.URL, meta, nil
}

// Convert audio stream URL to MP3
func convertToMP3(parentCtx context.Context, audioURL string) (string, error) {
    token := uuid.New().String()
    outputDir := downloadsDir

	if err := os.MkdirAll(outputDir, os.ModePerm); err != nil {
		return "", err
	}

	outputPath := filepath.Join(outputDir, token+".mp3")

	start := time.Now()

    ctx, cancel := context.WithTimeout(parentCtx, ffmpegTimeout)
    defer cancel()
    cmd := exec.CommandContext(ctx, ffmpegPath, "-y", "-i", audioURL, "-vn", "-ab", "192k", "-ar", "44100", "-f", "mp3", outputPath)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ffmpeg error: %v\nOutput: %s", err, out.String())
	}

	elapsed := time.Since(start)
    log.Printf("⏱️ Conversion time: %.2fs", elapsed.Seconds())

	return outputPath, nil
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
