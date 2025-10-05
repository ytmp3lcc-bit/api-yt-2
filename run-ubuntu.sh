#!/bin/bash

echo "========================================"
echo "Starting YouTube to MP3 API on Ubuntu"
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

# Check if API binary exists
if [ ! -f "./ytmp3-api" ]; then
    print_error "API binary not found. Please run setup-ubuntu.sh first"
    exit 1
fi

# Check if Redis is running
if command -v redis-cli &> /dev/null; then
    if redis-cli ping &> /dev/null; then
        print_status "Redis is running"
    else
        print_warning "Redis is not running. Starting Redis..."
        sudo systemctl start redis-server
        sleep 2
        if redis-cli ping &> /dev/null; then
            print_status "Redis started successfully"
        else
            print_warning "Redis failed to start. API will run in memory-only mode"
        fi
    fi
else
    print_warning "Redis not found. API will run in memory-only mode"
fi

# Check if required tools are installed
if ! command -v ffmpeg &> /dev/null; then
    print_error "FFmpeg not found. Please install it first: sudo apt install ffmpeg"
    exit 1
fi

if ! command -v yt-dlp &> /dev/null; then
    print_error "yt-dlp not found. Please install it first"
    exit 1
fi

# Create downloads directory if it doesn't exist
mkdir -p downloads
chmod 755 downloads

# Set environment variables
export REDIS_ADDR=localhost:6379
export WORKER_POOL_SIZE=20
export JOB_QUEUE_CAPACITY=1000
export REQUESTS_PER_SECOND=100

echo
print_status "Starting API server..."
echo "========================================"
echo "API will start on http://localhost:8080"
echo "Health check: http://localhost:8080/health"
echo "Metrics: http://localhost:8080/metrics"
echo "========================================"
echo
echo "Press Ctrl+C to stop the server"
echo

# Start the API
./ytmp3-api