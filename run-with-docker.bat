@echo off
echo ========================================
echo Running with Docker Desktop
echo ========================================

echo.
echo Checking if Docker is running...
docker version >nul 2>nul
if %errorlevel% neq 0 (
    echo Docker is not running or not installed.
    echo Please install Docker Desktop for Windows first.
    echo Download from: https://www.docker.com/products/docker-desktop/
    pause
    exit /b 1
)

echo.
echo Building and starting services...
docker-compose up -d

if %errorlevel% neq 0 (
    echo Docker Compose failed. Trying with 'docker compose'...
    docker compose up -d
)

echo.
echo Services started! Check status with:
echo docker-compose ps
echo.
echo API available at: http://localhost:8080
echo Redis at: localhost:6379
echo Prometheus at: http://localhost:9090
echo Grafana at: http://localhost:3000 (admin/admin123)
echo.

echo To stop services:
echo docker-compose down
echo.

pause