@echo off
echo ========================================
echo Installing Required Tools for Windows
echo ========================================

echo.
echo Installing yt-dlp...
echo -------------------
pip install yt-dlp
if %errorlevel% neq 0 (
    echo Failed to install yt-dlp via pip. Trying alternative method...
    echo Please download yt-dlp.exe from: https://github.com/yt-dlp/yt-dlp/releases
    echo and place it in the same directory as this script.
    pause
)

echo.
echo Installing FFmpeg...
echo -------------------
echo Please download FFmpeg from: https://ffmpeg.org/download.html
echo Or use chocolatey: choco install ffmpeg
echo Or use winget: winget install FFmpeg
echo.

echo Checking if tools are available...
where yt-dlp >nul 2>nul
if %errorlevel% equ 0 (
    echo ✅ yt-dlp found
) else (
    echo ❌ yt-dlp not found
)

where ffmpeg >nul 2>nul
if %errorlevel% equ 0 (
    echo ✅ ffmpeg found
) else (
    echo ❌ ffmpeg not found
)

echo.
echo After installing the tools, run setup-windows.bat
pause