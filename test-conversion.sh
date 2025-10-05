#!/bin/bash

echo "Testing YouTube conversion directly with yt-dlp..."

# Test URL
URL="https://www.youtube.com/watch?v=dQw4w9WgXcQ"

echo "Testing URL: $URL"
echo "========================================"

# Test 1: Basic yt-dlp
echo "Test 1: Basic yt-dlp"
yt-dlp --dump-json --no-warnings "$URL" 2>&1 | head -20

echo
echo "========================================"

# Test 2: With enhanced headers
echo "Test 2: With enhanced headers"
yt-dlp \
  -f "bestaudio" \
  --dump-json \
  --no-warnings \
  --user-agent "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36" \
  --referer "https://www.youtube.com/" \
  --add-header "Accept-Language:en-US,en;q=0.9" \
  --add-header "Accept:text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8" \
  --sleep-requests 1 \
  --sleep-interval 1 \
  --max-sleep-interval 3 \
  "$URL" 2>&1 | head -20

echo
echo "========================================"

# Test 3: Using config file
echo "Test 3: Using config file"
yt-dlp --dump-json --no-warnings "$URL" 2>&1 | head -20

echo
echo "========================================"
echo "Test completed"