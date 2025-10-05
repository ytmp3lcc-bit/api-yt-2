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
	URL string `json:"url"`
}

// Response structure
type Response struct {
	DownloadEndpoint string    `json:"download_endpoint"`
	Metadata         *Metadata `json:"metadata"`
	Token            string    `json:"token"`
}

func main() {
	http.HandleFunc("/extract", handleExtract)
	http.HandleFunc("/download/", handleDownload)

	fmt.Println("🚀 Server running on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
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

	// 🟢 Log: URL received
	fmt.Printf("\n🎬 Received URL: %s\n", videoURL)

	// 1️⃣ Extract direct audio stream URL via yt-dlp
	fmt.Println("🔍 Step 1: Extracting audio stream link using yt-dlp...")
	audioURL, meta, err := getAudioStream(videoURL)
	if err != nil {
		fmt.Printf("❌ yt-dlp error: %v\n", err)
		http.Error(w, fmt.Sprintf("yt-dlp error: %v", err), http.StatusInternalServerError)
		return
	}
	fmt.Println("✅ Audio stream extracted successfully!")

	// 2️⃣ Convert stream to MP3 file using ffmpeg
	fmt.Println("🎧 Step 2: Converting stream to MP3 using FFmpeg...")
	filePath, err := convertToMP3(audioURL)
	if err != nil {
		fmt.Printf("❌ ffmpeg error: %v\n", err)
		http.Error(w, fmt.Sprintf("ffmpeg error: %v", err), http.StatusInternalServerError)
		return
	}
	fmt.Printf("✅ Conversion completed successfully: %s\n", filePath)

	token := filepath.Base(filePath)
	meta.AudioURL = audioURL

	response := Response{
		DownloadEndpoint: fmt.Sprintf("http://localhost:8080/download/%s", token),
		Metadata:         meta,
		Token:            token,
	}

	fmt.Printf("📦 Step 3: File ready for download at: %s\n", response.DownloadEndpoint)
	fmt.Println("------------------------------------------------------------")

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// Use yt-dlp to get audio stream URL + metadata
func getAudioStream(videoURL string) (string, *Metadata, error) {
	cmd := exec.Command("./yt-dlp", "-f", "bestaudio", "--dump-single-json", "--no-warnings", videoURL)
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
func convertToMP3(audioURL string) (string, error) {
	token := uuid.New().String()
	outputDir := "downloads"

	if err := os.MkdirAll(outputDir, os.ModePerm); err != nil {
		return "", err
	}

	outputPath := filepath.Join(outputDir, token+".mp3")

	start := time.Now()

	cmd := exec.Command("./ffmpeg", "-y", "-i", audioURL, "-vn", "-ab", "192k", "-ar", "44100", "-f", "mp3", outputPath)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ffmpeg error: %v\nOutput: %s", err, out.String())
	}

	elapsed := time.Since(start)
	fmt.Printf("⏱️ Conversion time: %.2fs\n", elapsed.Seconds())

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
	filePath := filepath.Join("downloads", token)

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

	fmt.Printf("⬇️  File requested for download: %s\n", token)

	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s.mp3\"", token))
	io.Copy(w, file)
}
