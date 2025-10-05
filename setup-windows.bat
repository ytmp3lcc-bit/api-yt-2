@echo off
echo ========================================
echo YouTube to MP3 API - Windows Setup
echo ========================================

echo.
echo Step 1: Installing dependencies...
go mod tidy

echo.
echo Step 2: Checking if Redis is available...
where redis-server >nul 2>nul
if %errorlevel% neq 0 (
    echo Redis not found. Please install Redis for Windows:
    echo 1. Download from: https://github.com/microsoftarchive/redis/releases
    echo 2. Or use WSL: wsl --install
    echo 3. Or use Docker Desktop
    echo.
    echo For now, the API will run without Redis (in-memory mode)
    pause
) else (
    echo Redis found! Starting Redis server...
    start "Redis Server" redis-server
    timeout /t 3 /nobreak >nul
)

echo.
echo Step 3: Building the API...
go build -o ytmp3-api.exe main_optimized.go

if %errorlevel% neq 0 (
    echo Build failed! Please check the errors above.
    pause
    exit /b 1
)

echo.
echo Step 4: Creating downloads directory...
if not exist downloads mkdir downloads

echo.
echo Step 5: Starting the API server...
echo ========================================
echo API will start on http://localhost:8080
echo Health check: http://localhost:8080/health
echo Metrics: http://localhost:8080/metrics
echo ========================================
echo.
echo Press Ctrl+C to stop the server
echo.

ytmp3-api.exe

pause