@echo off
echo ========================================
echo Redis Installation for Windows
echo ========================================

echo.
echo Option 1: Using Chocolatey (Recommended)
echo ----------------------------------------
echo 1. Install Chocolatey first: https://chocolatey.org/install
echo 2. Then run: choco install redis-64
echo.

echo Option 2: Using WSL (Windows Subsystem for Linux)
echo ------------------------------------------------
echo 1. Open PowerShell as Administrator
echo 2. Run: wsl --install
echo 3. After restart, open WSL and run:
echo    sudo apt update
echo    sudo apt install redis-server
echo    sudo service redis-server start
echo.

echo Option 3: Using Docker Desktop
echo ------------------------------
echo 1. Install Docker Desktop for Windows
echo 2. Run: docker run -d -p 6379:6379 redis:alpine
echo.

echo Option 4: Manual Download
echo -------------------------
echo 1. Download from: https://github.com/microsoftarchive/redis/releases
echo 2. Extract and run redis-server.exe
echo.

echo After installing Redis, run setup-windows.bat again
pause