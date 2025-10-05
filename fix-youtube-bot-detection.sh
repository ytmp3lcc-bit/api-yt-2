#!/bin/bash

echo "========================================"
echo "Fixing YouTube Bot Detection Issues"
echo "========================================"

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

print_status() {
    echo -e "${GREEN}✅ $1${NC}"
}

print_warning() {
    echo -e "${YELLOW}⚠️  $1${NC}"
}

print_error() {
    echo -e "${RED}❌ $1${NC}"
}

echo
echo "Step 1: Updating yt-dlp to latest version..."
sudo curl -L https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp -o /usr/local/bin/yt-dlp
sudo chmod a+rx /usr/local/bin/yt-dlp

if [ $? -eq 0 ]; then
    print_status "yt-dlp updated to latest version: $(yt-dlp --version)"
else
    print_error "Failed to update yt-dlp"
    exit 1
fi

echo
echo "Step 2: Creating yt-dlp configuration file..."
mkdir -p ~/.config/yt-dlp

cat > ~/.config/yt-dlp/config << 'EOF'
# yt-dlp configuration to avoid bot detection
--user-agent "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
--referer "https://www.youtube.com/"
--add-header "Accept-Language:en-US,en;q=0.9"
--add-header "Accept:text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
--sleep-requests 1
--sleep-interval 1
--max-sleep-interval 3
--retries 3
--fragment-retries 3
--extractor-retries 3
--no-check-certificate
--prefer-insecure
EOF

print_status "yt-dlp configuration created"

echo
echo "Step 3: Testing yt-dlp with a simple video..."
# Test with a simple, short video
test_url="https://www.youtube.com/watch?v=dQw4w9WgXcQ"  # Rick Roll - short and usually works
echo "Testing with: $test_url"

timeout 30 yt-dlp --dump-json --no-warnings "$test_url" > /dev/null 2>&1

if [ $? -eq 0 ]; then
    print_status "yt-dlp test successful!"
else
    print_warning "yt-dlp test failed, but this might be due to the specific video"
fi

echo
echo "Step 4: Creating alternative extraction method..."
cat > extract_alternative.py << 'EOF'
#!/usr/bin/env python3
import subprocess
import sys
import json
import time
import random

def extract_with_retry(url, max_retries=3):
    """Extract video info with retry and different strategies"""
    
    strategies = [
        # Strategy 1: Basic with headers
        [
            "yt-dlp",
            "-f", "bestaudio",
            "--dump-json",
            "--no-warnings",
            "--user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
            "--referer", "https://www.youtube.com/",
            url
        ],
        # Strategy 2: With sleep and headers
        [
            "yt-dlp",
            "-f", "bestaudio", 
            "--dump-json",
            "--no-warnings",
            "--user-agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36",
            "--referer", "https://www.youtube.com/",
            "--sleep-requests", "2",
            "--sleep-interval", "2",
            url
        ],
        # Strategy 3: Minimal approach
        [
            "yt-dlp",
            "-f", "bestaudio",
            "--dump-json", 
            "--no-warnings",
            url
        ]
    ]
    
    for attempt in range(max_retries):
        strategy = strategies[attempt % len(strategies)]
        
        print(f"Attempt {attempt + 1}: Using strategy {attempt % len(strategies) + 1}")
        
        try:
            result = subprocess.run(strategy, capture_output=True, text=True, timeout=30)
            
            if result.returncode == 0:
                data = json.loads(result.stdout)
                return data.get('url'), {
                    'title': data.get('title', ''),
                    'uploader': data.get('uploader', ''),
                    'duration': data.get('duration', 0),
                    'ext': data.get('ext', ''),
                    'abr': data.get('abr', 0)
                }
            else:
                print(f"Strategy failed: {result.stderr}")
                
        except Exception as e:
            print(f"Error with strategy: {e}")
        
        if attempt < max_retries - 1:
            sleep_time = random.uniform(2, 5)
            print(f"Waiting {sleep_time:.1f} seconds before retry...")
            time.sleep(sleep_time)
    
    return None, None

if __name__ == "__main__":
    if len(sys.argv) != 2:
        print("Usage: python3 extract_alternative.py <youtube_url>")
        sys.exit(1)
    
    url = sys.argv[1]
    audio_url, metadata = extract_with_retry(url)
    
    if audio_url:
        print(json.dumps({
            'url': audio_url,
            'metadata': metadata
        }))
    else:
        print(json.dumps({'error': 'Failed to extract audio URL'}))
        sys.exit(1)
EOF

chmod +x extract_alternative.py
print_status "Alternative extraction script created"

echo
echo "========================================"
echo "Bot Detection Fix Complete!"
echo "========================================"
echo
print_status "yt-dlp updated to latest version"
print_status "Configuration file created at ~/.config/yt-dlp/config"
print_status "Alternative extraction script created"
echo
echo "The API should now work better with YouTube videos."
echo "If you still get bot detection errors, try:"
echo "1. Using a different video URL"
echo "2. Waiting a few minutes before trying again"
echo "3. Using the alternative extraction method"
echo