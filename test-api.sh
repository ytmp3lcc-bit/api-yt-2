#!/bin/bash

echo "========================================"
echo "YouTube to MP3 API Test Script"
echo "========================================"

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

print_success() {
    echo -e "${GREEN}✅ $1${NC}"
}

print_error() {
    echo -e "${RED}❌ $1${NC}"
}

print_warning() {
    echo -e "${YELLOW}⚠️  $1${NC}"
}

# Check if API is running
echo "Checking API status..."
if curl -s http://localhost:8080/health > /dev/null; then
    print_success "API is running"
else
    print_error "API is not running. Please start it with: ./ytmp3-api &"
    exit 1
fi

echo
echo "Testing with a sample YouTube video..."

# Test with Rick Roll (short and reliable)
VIDEO_URL="https://www.youtube.com/watch?v=dQw4w9WgXcQ"
echo "URL: $VIDEO_URL"

# Submit conversion request
echo "Submitting conversion request..."
RESPONSE=$(curl -s -X POST -H "Content-Type: application/json" -d "{\"url\":\"$VIDEO_URL\"}" http://localhost:8080/extract)

if [ $? -eq 0 ]; then
    print_success "Request submitted successfully"
    
    # Extract job ID
    JOB_ID=$(echo $RESPONSE | grep -o '"job_id":"[^"]*"' | cut -d'"' -f4)
    echo "Job ID: $JOB_ID"
    
    if [ -n "$JOB_ID" ]; then
        echo
        echo "Checking job status..."
        
        # Wait and check status
        for i in {1..10}; do
            STATUS_RESPONSE=$(curl -s http://localhost:8080/status/$JOB_ID)
            STATUS=$(echo $STATUS_RESPONSE | grep -o '"status":"[^"]*"' | cut -d'"' -f4)
            
            echo "Attempt $i: Status = $STATUS"
            
            if [ "$STATUS" = "completed" ]; then
                print_success "Conversion completed!"
                
                # Extract download URL
                DOWNLOAD_URL=$(echo $STATUS_RESPONSE | grep -o '"download_url":"[^"]*"' | cut -d'"' -f4)
                echo "Download URL: $DOWNLOAD_URL"
                
                # Test download
                echo "Testing download..."
                if curl -I -s "$DOWNLOAD_URL" | head -1 | grep -q "200 OK"; then
                    print_success "Download link is working!"
                    echo "You can download the MP3 file from: $DOWNLOAD_URL"
                else
                    print_error "Download link is not working"
                fi
                break
            elif [ "$STATUS" = "failed" ]; then
                print_error "Conversion failed"
                echo "Error details: $STATUS_RESPONSE"
                break
            else
                echo "Waiting for conversion to complete..."
                sleep 3
            fi
        done
        
        if [ "$STATUS" != "completed" ] && [ "$STATUS" != "failed" ]; then
            print_warning "Conversion is taking longer than expected. Check status manually:"
            echo "curl http://localhost:8080/status/$JOB_ID"
        fi
    else
        print_error "Could not extract job ID from response"
    fi
else
    print_error "Failed to submit conversion request"
fi

echo
echo "========================================"
echo "Test completed!"
echo "========================================"