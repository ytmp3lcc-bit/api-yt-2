# YouTube to MP3 API - Ubuntu Setup

This guide will help you run the high-traffic YouTube to MP3 API on Ubuntu.

## 🚀 Quick Start

### Option 1: Direct Installation (Recommended)

```bash
# 1. Make scripts executable
chmod +x *.sh

# 2. Run setup script
./setup-ubuntu.sh

# 3. Start the API
./run-ubuntu.sh
```

### Option 2: Docker Installation

```bash
# 1. Install Docker and Docker Compose
sudo apt install docker.io docker-compose

# 2. Start services
docker-compose -f docker-compose-ubuntu.yml up -d

# 3. Check status
docker-compose -f docker-compose-ubuntu.yml ps
```

## 📋 Prerequisites

- Ubuntu 20.04 or later
- Internet connection
- sudo privileges

## 🔧 What Gets Installed

### System Packages
- Go 1.21.5
- FFmpeg
- yt-dlp
- Redis (optional but recommended)
- Build tools

### API Features
- 20 concurrent workers
- 1000 job queue capacity
- 100 requests/second rate limit
- Redis caching
- Health monitoring
- Graceful shutdown

## 📊 API Endpoints

- **Health Check**: `http://localhost:8080/health`
- **Metrics**: `http://localhost:8080/metrics`
- **Stats**: `http://localhost:8080/stats`
- **Extract**: `POST http://localhost:8080/extract`
- **Status**: `GET http://localhost:8080/status/{job_id}`
- **Download**: `GET http://localhost:8080/download/{job_id}.mp3`

## 🎯 Usage Examples

### Start Conversion
```bash
curl -X POST http://localhost:8080/extract \
  -H "Content-Type: application/json" \
  -d '{"url": "https://www.youtube.com/watch?v=VIDEO_ID"}'
```

### Check Status
```bash
curl http://localhost:8080/status/JOB_ID
```

### Download File
```bash
curl -O http://localhost:8080/download/JOB_ID.mp3
```

## 🔧 Configuration

### Environment Variables
```bash
export REDIS_ADDR=localhost:6379
export WORKER_POOL_SIZE=20
export JOB_QUEUE_CAPACITY=1000
export REQUESTS_PER_SECOND=100
```

### Systemd Service
The setup script can create a systemd service for production use:

```bash
# Start service
sudo systemctl start ytmp3-api

# Check status
sudo systemctl status ytmp3-api

# View logs
sudo journalctl -u ytmp3-api -f
```

## 📈 Performance

### Expected Capacity
- **Concurrent Jobs**: 20 active + 1000 queued
- **Request Rate**: 100 req/s sustained
- **Memory Usage**: ~2GB per instance
- **CPU Usage**: ~2 cores per instance

### Monitoring
- Real-time metrics at `/metrics`
- Health status at `/health`
- Application stats at `/stats`

## 🐳 Docker Setup

### Using Docker Compose
```bash
# Start all services
docker-compose -f docker-compose-ubuntu.yml up -d

# Scale API instances
docker-compose -f docker-compose-ubuntu.yml up -d --scale api=3

# View logs
docker-compose -f docker-compose-ubuntu.yml logs -f api

# Stop services
docker-compose -f docker-compose-ubuntu.yml down
```

### Services Included
- **API**: Main application
- **Redis**: Caching and job storage
- **Nginx**: Load balancer and reverse proxy

## 🔍 Troubleshooting

### Common Issues

1. **Permission Denied**
   ```bash
   chmod +x *.sh
   sudo chown -R $USER:$USER .
   ```

2. **Port Already in Use**
   ```bash
   sudo lsof -i :8080
   sudo kill -9 PID
   ```

3. **Redis Connection Failed**
   ```bash
   sudo systemctl start redis-server
   redis-cli ping
   ```

4. **FFmpeg Not Found**
   ```bash
   sudo apt install ffmpeg
   ```

5. **yt-dlp Not Found**
   ```bash
   sudo curl -L https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp -o /usr/local/bin/yt-dlp
   sudo chmod a+rx /usr/local/bin/yt-dlp
   ```

### Logs and Debugging

```bash
# View API logs
./run-ubuntu.sh

# View systemd logs
sudo journalctl -u ytmp3-api -f

# View Docker logs
docker-compose -f docker-compose-ubuntu.yml logs -f api

# Check Redis
redis-cli monitor
```

## 🔒 Security Considerations

- Run as non-root user
- Use firewall to restrict access
- Enable HTTPS in production
- Implement authentication
- Monitor resource usage

## 📝 Production Deployment

### 1. Use Systemd Service
```bash
sudo systemctl enable ytmp3-api
sudo systemctl start ytmp3-api
```

### 2. Configure Nginx
```bash
sudo cp nginx-ubuntu.conf /etc/nginx/sites-available/ytmp3-api
sudo ln -s /etc/nginx/sites-available/ytmp3-api /etc/nginx/sites-enabled/
sudo nginx -t
sudo systemctl reload nginx
```

### 3. Set up Monitoring
- Configure log rotation
- Set up health checks
- Monitor resource usage
- Set up alerts

## 🎉 Success!

Your YouTube to MP3 API is now running on Ubuntu with high-traffic optimizations!

- **API**: http://localhost:8080
- **Health**: http://localhost:8080/health
- **Metrics**: http://localhost:8080/metrics