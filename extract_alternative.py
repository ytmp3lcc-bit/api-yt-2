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
