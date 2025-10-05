#!/bin/bash

echo "========================================"
echo "YouTube to MP3 API - Ubuntu Setup"
echo "========================================"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Function to print colored output
print_status() {
    echo -e "${GREEN}✅ $1${NC}"
}

print_warning() {
    echo -e "${YELLOW}⚠️  $1${NC}"
}

print_error() {
    echo -e "${RED}❌ $1${NC}"
}

# Check if running as root
if [[ $EUID -eq 0 ]]; then
   print_error "This script should not be run as root for security reasons"
   exit 1
fi

echo
echo "Step 1: Updating system packages..."
sudo apt update && sudo apt upgrade -y

echo
echo "Step 2: Installing required system packages..."
sudo apt install -y \
    curl \
    wget \
    git \
    build-essential \
    software-properties-common \
    apt-transport-https \
    ca-certificates \
    gnupg \
    lsb-release

print_status "System packages installed"

echo
echo "Step 3: Installing Go..."
# Check if Go is already installed
if ! command -v go &> /dev/null; then
    # Install Go 1.21
    GO_VERSION="1.21.5"
    wget https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz
    sudo tar -C /usr/local -xzf go${GO_VERSION}.linux-amd64.tar.gz
    rm go${GO_VERSION}.linux-amd64.tar.gz
    
    # Add Go to PATH
    echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
    echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.profile
    export PATH=$PATH:/usr/local/go/bin
    
    print_status "Go ${GO_VERSION} installed"
else
    print_status "Go is already installed: $(go version)"
fi

echo
echo "Step 4: Installing FFmpeg..."
if ! command -v ffmpeg &> /dev/null; then
    sudo apt install -y ffmpeg
    print_status "FFmpeg installed"
else
    print_status "FFmpeg is already installed: $(ffmpeg -version | head -n1)"
fi

echo
echo "Step 5: Installing yt-dlp..."
if ! command -v yt-dlp &> /dev/null; then
    sudo curl -L https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp -o /usr/local/bin/yt-dlp
    sudo chmod a+rx /usr/local/bin/yt-dlp
    print_status "yt-dlp installed"
else
    print_status "yt-dlp is already installed: $(yt-dlp --version)"
fi

echo
echo "Step 6: Installing Redis (optional but recommended)..."
if ! command -v redis-server &> /dev/null; then
    sudo apt install -y redis-server
    sudo systemctl enable redis-server
    sudo systemctl start redis-server
    print_status "Redis installed and started"
else
    print_status "Redis is already installed"
    sudo systemctl start redis-server
fi

echo
echo "Step 7: Setting up the API..."
# Create downloads directory
mkdir -p downloads
chmod 755 downloads

# Install Go dependencies
if [ -f "go_windows.mod" ]; then
    # Use Windows mod file as base and rename
    cp go_windows.mod go.mod
    print_status "Using Windows mod file as base"
fi

# Install dependencies
go mod tidy

echo
echo "Step 8: Building the API..."
go build -o ytmp3-api main_windows.go

if [ $? -eq 0 ]; then
    print_status "API built successfully"
else
    print_error "Build failed! Please check the errors above."
    exit 1
fi

echo
echo "Step 9: Creating systemd service (optional)..."
read -p "Do you want to create a systemd service for production? (y/n): " -n 1 -r
echo
if [[ $REPLY =~ ^[Yy]$ ]]; then
    sudo tee /etc/systemd/system/ytmp3-api.service > /dev/null <<EOF
[Unit]
Description=YouTube to MP3 API
After=network.target redis.service

[Service]
Type=simple
User=$USER
WorkingDirectory=$(pwd)
ExecStart=$(pwd)/ytmp3-api
Restart=always
RestartSec=5
Environment=REDIS_ADDR=localhost:6379

[Install]
WantedBy=multi-user.target
EOF

    sudo systemctl daemon-reload
    sudo systemctl enable ytmp3-api
    print_status "Systemd service created and enabled"
fi

echo
echo "========================================"
echo "Setup Complete!"
echo "========================================"
echo
print_status "API binary: $(pwd)/ytmp3-api"
print_status "Downloads directory: $(pwd)/downloads"
print_status "Redis status: $(systemctl is-active redis-server)"
echo
echo "To start the API:"
echo "  ./ytmp3-api"
echo
echo "To start as service:"
echo "  sudo systemctl start ytmp3-api"
echo "  sudo systemctl status ytmp3-api"
echo
echo "API will be available at: http://localhost:8080"
echo "Health check: http://localhost:8080/health"
echo "Metrics: http://localhost:8080/metrics"
echo