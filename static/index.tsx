
type VideoInfo = {
    id: string; // YouTube Video ID
    title: string;
    thumbnail: string;
    duration?: number; // Optional duration in seconds
    format: 'MP3';
    url: string; // Original YouTube URL
    conversionDate: string; // ISO string
};

type AppState = {
    theme: 'light' | 'dark';
    conversionStatus: 'idle' | 'loading' | 'processing' | 'completed' | 'error';
    errorMessage: string;
    processingMessage: string;
    videoInfo: Partial<Omit<VideoInfo, 'url' | 'conversionDate'>> | null;
    history: VideoInfo[];
    currentUrl: string;
    downloadUrl: string | null;
    currentJobId: string | null;
    hasShownCoffeePrompt: boolean;
};

document.addEventListener('DOMContentLoaded', () => {
    // --- API Configuration ---
    const BASE_URL = 'http://localhost:8080'; // New API base URL
    const LOCAL_STORAGE_HISTORY_KEY = 'conversionHistory_v1';
    
    // --- Coffee Prompt Configuration ---
    const FIRST_COFFEE_PROMPT_THRESHOLD = 4;
    const FINAL_COFFEE_PROMPT_THRESHOLD = 50;
    
    // --- Page Detection ---
    const isMainPage = !!document.getElementById('converter-form');


    // --- DOM ELEMENT REFERENCES ---
    const dom = {
        // Shared elements (available on all pages)
        html: document.documentElement,
        themeToggleCheckbox: document.getElementById('switch') as HTMLInputElement,
        historyToggleBtn: document.getElementById('history-toggle-btn')!,
        historyNotificationDot: document.getElementById('history-notification-dot')!,
        historyModalOverlay: document.getElementById('history-modal-overlay')!,
        closeHistoryModalBtn: document.getElementById('close-history-modal-btn')!,
        historyContainer: document.getElementById('history-container')!,
        historyEmptyState: document.getElementById('history-empty-state')!,
        clearHistoryBtn: document.getElementById('clear-history-btn')!,
        shortcutsToggleBtn: document.getElementById('shortcuts-toggle-btn')!,
        shortcutsModalOverlay: document.getElementById('shortcuts-modal-overlay')!,
        closeShortcutsModalBtn: document.getElementById('close-shortcuts-modal-btn')!,

        // Main page specific elements (nullable)
        formContainer: document.getElementById('converter-form-container'),
        form: document.getElementById('converter-form') as HTMLFormElement | null,
        urlInput: document.getElementById('url-input') as HTMLInputElement | null,
        inputRow: document.getElementById('input-row'),
        pasteBtn: document.getElementById('paste-btn'),
        clearBtn: document.getElementById('clear-btn'),
        errorMessageContainer: document.getElementById('error-message-container'),
        
        resultCard: document.getElementById('result-card-container'),
        resultContentArea: document.getElementById('result-content-area'),
        resultSkeletonArea: document.getElementById('result-skeleton-area'),
        resultThumbnail: document.getElementById('result-thumbnail') as HTMLImageElement | null,
        resultTitle: document.getElementById('result-title'),
        resultDurationWrapper: document.getElementById('result-duration-wrapper'),
        resultDuration: document.getElementById('result-duration'),
        resultStatus: document.getElementById('result-status'),
        
        resultActions: document.getElementById('result-actions-container'),
        convertAnotherBtn: document.getElementById('convert-another-btn'),
        downloadBtn: document.getElementById('download-btn') as HTMLAnchorElement | null,
        downloadBtnText: document.getElementById('download-btn-text'),
        downloadBtnLoader: document.getElementById('download-btn-loader'),
        buyCoffeeBtn: document.getElementById('buy-coffee-btn') as HTMLAnchorElement | null,

        promoBuyCoffee: document.getElementById('promo-buy-coffee'),

        shareTwitter: document.getElementById('share-twitter') as HTMLAnchorElement | null,
        shareFacebook: document.getElementById('share-facebook') as HTMLAnchorElement | null,
        shareWhatsapp: document.getElementById('share-whatsapp') as HTMLAnchorElement | null,
        copyLinkBtn: document.getElementById('copy-link-btn') as HTMLButtonElement | null,
        copyLinkIconDefault: document.getElementById('copy-link-icon-default'),
        copyLinkIconSuccess: document.getElementById('copy-link-icon-success'),
        
        coffeeModalOverlay: document.getElementById('coffee-modal-overlay'),
        coffeeModalCloseBtn: document.getElementById('coffee-modal-close-btn'),
        coffeeModalCTA: document.getElementById('coffee-modal-cta') as HTMLAnchorElement | null,

        welcomeOverlay: document.getElementById('welcome-overlay'),
        welcomeCard: document.getElementById('welcome-card'),
        welcomeStartBtn: document.getElementById('welcome-start-btn'),

        faqContainer: document.getElementById('faq-container'),
    };

    // --- STATE MANAGEMENT ---
    let state: AppState = {
        theme: 'dark',
        conversionStatus: 'idle',
        errorMessage: '',
        processingMessage: '',
        videoInfo: null,
        history: [],
        currentUrl: '',
        downloadUrl: null,
        currentJobId: null,
        hasShownCoffeePrompt: false,
    };

    let processingMessageTimeouts: ReturnType<typeof setTimeout>[] = [];
    let pollingInterval: number | null = null;


    // --- UTILITY FUNCTIONS ---
    
    /**
     * Extracts the YouTube video ID from various URL formats.
     * @param url The YouTube URL.
     * @returns The 11-character video ID or null if not found.
     */
    function extractYouTubeVideoId(url: string): string | null {
        if (!url) return null;
        const regex = /(?:youtube\.com\/(?:[^\/]+\/.+\/|(?:v|e(?:mbed)?)\/|.*[?&]v=)|youtu\.be\/|youtube\.com\/shorts\/)([a-zA-Z0-9_-]{11})/;
        const match = url.match(regex);
        return match ? match[1] : null;
    }

    const isValidYouTubeUrl = (url: string) => !!extractYouTubeVideoId(url);
    
    function timeAgo(isoDate: string): string {
        const date = new Date(isoDate);
        const now = new Date();
        const seconds = Math.round((now.getTime() - date.getTime()) / 1000);
        const minutes = Math.round(seconds / 60);
        const hours = Math.round(minutes / 60);
        const days = Math.round(hours / 24);

        if (seconds < 60) return `${seconds} sec ago`;
        if (minutes < 60) return `${minutes} min ago`;
        if (hours < 24) return `${hours} hr ago`;
        if (days < 7) return `${days} day${days > 1 ? 's' : ''} ago`;
        
        return date.toLocaleDateString('en-US', {
            month: 'short',
            day: 'numeric',
            year: 'numeric'
        });
    }

    // --- RENDER FUNCTIONS ---
    function render() {
        if (!isMainPage) return; // Only render main page UI
        dom.formContainer!.classList.toggle('active', state.conversionStatus === 'idle' || state.conversionStatus === 'error');
        dom.resultCard!.classList.toggle('active', !['idle', 'error'].includes(state.conversionStatus));
        
        renderForm();
        renderResult();
        renderHistory();
    }
    
    function renderForm() {
        if (!isMainPage) return;
        const hasError = !!state.errorMessage;
        
        if (hasError) {
            dom.errorMessageContainer!.textContent = state.errorMessage;
            dom.errorMessageContainer!.style.display = 'block';
            dom.inputRow!.classList.remove('border-light-border', 'dark:border-dark-border');
            dom.inputRow!.classList.add('border-light-error-border', 'dark:border-error-border');
            dom.inputRow!.classList.add('has-error-shake');
        } else {
            dom.errorMessageContainer!.style.display = 'none';
            dom.inputRow!.classList.remove('border-light-error-border', 'dark:border-error-border');
            dom.inputRow!.classList.add('border-light-border', 'dark:border-dark-border');
            dom.inputRow!.classList.remove('has-error-shake');
        }

        const urlValue = dom.urlInput!.value.trim();
        const isValid = isValidYouTubeUrl(urlValue);
        dom.inputRow!.classList.toggle('is-valid', isValid && !hasError);

        // --- In-field Icon Logic ---
        const showPaste = urlValue.length === 0;
        const showClear = urlValue.length > 0;

        dom.pasteBtn!.style.display = showPaste ? 'block' : 'none';
        dom.clearBtn!.style.display = showClear ? 'block' : 'none';
    }

    function renderResult() {
        if (!isMainPage) return;
        const isLoading = state.conversionStatus === 'loading';
        const isProcessing = state.conversionStatus === 'processing';
        const isCompleted = state.conversionStatus === 'completed';

        dom.resultSkeletonArea!.style.display = isLoading ? 'flex' : 'none';
        
        const showContent = (isProcessing || isCompleted) && !!state.videoInfo;
        dom.resultContentArea!.style.display = showContent ? 'flex' : 'none';
        dom.resultActions!.style.display = showContent ? 'flex' : 'none';
        
        if (showContent) {
            const { title, thumbnail, duration } = state.videoInfo;
            dom.resultThumbnail!.src = thumbnail || '';
            dom.resultTitle!.textContent = title || 'Loading title...';
            dom.resultTitle!.title = title || '';

            if (duration) {
                const minutes = Math.floor(duration / 60);
                const seconds = (duration % 60).toString().padStart(2, '0');
                dom.resultDuration!.textContent = `${minutes}:${seconds}`;
                dom.resultDurationWrapper!.style.display = 'inline';
            } else {
                dom.resultDurationWrapper!.style.display = 'none';
            }

            if (isProcessing) {
                dom.resultStatus!.innerHTML = `
                    <div class="flex items-center justify-end sm:justify-start gap-2 text-light-text-secondary dark:text-dark-text-secondary">
                        <div class="jimu-loader text-accent" aria-hidden="true">
                            <div class="jimu-primary-loading"></div>
                        </div>
                        <span>${state.processingMessage}</span>
                    </div>
                `;
            } else if (isCompleted) {
                dom.resultStatus!.innerHTML = `
                    <div class="flex items-center justify-end sm:justify-start gap-2">
                        <svg class="w-5 h-5 text-success dark:text-light-success flex-shrink-0" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 20" fill="currentColor" aria-hidden="true">
                            <path fill-rule="evenodd" d="M10 18a8 8 0 100-16 8 8 0 000 16zm3.707-9.293a1 1 0 00-1.414-1.414L9 10.586 7.707 9.293a1 1 0 00-1.414 1.414l2 2a1 1 0 001.414 0l4-4z" clip-rule="evenodd" />
                        </svg>
                        <span class="font-bold text-light-text-secondary dark:text-dark-text-secondary">Completed</span>
                    </div>
                `;
                
                const downloadButton = dom.downloadBtn!;
                const filename = `${state.videoInfo.title}.mp3`;
                downloadButton.title = `Download: ${filename}`;
                downloadButton.href = state.downloadUrl!;
                downloadButton.download = filename;

                if (!downloadButton.dataset.hasAnimated) {
                    downloadButton.classList.add('animate-glow-pulse');
                    downloadButton.dataset.hasAnimated = 'true';
                    downloadButton.addEventListener('animationend', () => {
                        downloadButton.classList.remove('animate-glow-pulse');
                    }, { once: true });
                }

                requestAnimationFrame(() => downloadButton.focus());
            }

            dom.downloadBtn!.style.display = isCompleted ? 'flex' : 'none';
            dom.convertAnotherBtn!.style.display = isCompleted ? 'block' : 'none';
            dom.buyCoffeeBtn!.style.display = isCompleted ? 'flex' : 'none';
        }
    }

    function renderHistory() {
        const hasHistory = state.history.length > 0;
        dom.historyContainer.style.display = hasHistory ? 'block' : 'none';
        dom.clearHistoryBtn.style.display = hasHistory ? 'flex' : 'none';
        dom.historyEmptyState.style.display = hasHistory ? 'none' : 'block';
        dom.historyNotificationDot.classList.toggle('hidden', !hasHistory);

        if (!hasHistory) {
            dom.historyContainer.innerHTML = ''; // Clear just in case
            return;
        }

        dom.historyContainer.innerHTML = '';

        const mp3Icon = `<svg aria-hidden="true" class="w-5 h-5 mr-2 text-light-text-secondary dark:text-dark-text-secondary" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor"><path d="M12 3v10.55c-.59-.34-1.27-.55-2-.55-2.21 0-4 1.79-4 4s1.79 4 4 4 4-1.79 4-4V7h4V3h-6z"/></svg>`;
        const regenerateIcon = `<svg class="w-5 h-5" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor"><path d="M12 4V1L8 5l4 4V6c3.31 0 6 2.69 6 6s-2.69 6-6 6-6-2.69-6-6H4c0 4.42 3.58 8 8 8s8-3.58 8-8-3.58-8-8-8z"/></svg>`;
        const deleteIcon = `<svg class="w-5 h-5" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor"><path d="M6 19c0 1.1.9 2 2 2h8c1.1 0 2-.9 2-2V7H6v12zM19 4h-3.5l-1-1h-5l-1 1H5v2h14V4z"/></svg>`;

        state.history.forEach((item) => {
            const historyItem = document.createElement('div');
            historyItem.className = 'flex items-center bg-light-surface-light dark:bg-dark-surface-light p-3 rounded-lg';
            
            historyItem.innerHTML = `
                <img src="${item.thumbnail}" alt="Thumbnail for ${item.title}" class="w-16 h-9 rounded-md object-cover mr-4 flex-shrink-0" loading="lazy">
                <div class="flex-grow min-w-0">
                    <p class="font-semibold truncate text-light-text dark:text-dark-text">${item.title}</p>
                    <div class="flex items-center text-sm text-light-text-secondary dark:text-dark-text-secondary flex-wrap">
                        ${mp3Icon} MP3
                        <span class="mx-2">&#8226;</span>
                        <span>${timeAgo(item.conversionDate)}</span>
                    </div>
                </div>
                <div class="flex items-center ml-4 space-x-1 flex-shrink-0">
                    <button data-history-id="${item.id}" class="regenerate-btn p-2 rounded-full hover:bg-light-button-secondary-bg dark:hover:bg-dark-button-secondary-bg" aria-label="Convert ${item.title} again">
                        ${regenerateIcon}
                    </button>
                    <button data-history-id="${item.id}" class="delete-history-btn p-2 rounded-full hover:bg-light-button-secondary-bg dark:hover:bg-dark-button-secondary-bg" aria-label="Delete ${item.title} from history">
                        ${deleteIcon}
                    </button>
                </div>
            `;
            dom.historyContainer.appendChild(historyItem);
        });
    }

    // --- Add missing history management functions ---
    function loadHistory() {
        try {
            const historyJson = localStorage.getItem(LOCAL_STORAGE_HISTORY_KEY);
            if (historyJson) {
                const parsedHistory = JSON.parse(historyJson);
                if (Array.isArray(parsedHistory)) {
                    state.history = parsedHistory;
                }
            }
        } catch (e) {
            console.error('Failed to load history from localStorage:', e);
            state.history = [];
        }
    }
    
    function saveHistory() {
        try {
            localStorage.setItem(LOCAL_STORAGE_HISTORY_KEY, JSON.stringify(state.history));
        } catch (e) {
            console.error('Failed to save history to localStorage:', e);
        }
    }

    function addToHistory(item: VideoInfo) {
        // Remove any existing entry with the same ID to avoid duplicates and bring the latest conversion to the top.
        const existingIndex = state.history.findIndex(h => h.id === item.id);
        if (existingIndex > -1) {
            state.history.splice(existingIndex, 1);
        }

        // Add the new item to the beginning of the history array.
        state.history.unshift(item);

        // Persist the updated history to local storage.
        saveHistory();
    }
            
    // --- EVENT HANDLERS & LOGIC ---
    function applyTheme(theme: 'light' | 'dark') {
        state.theme = theme;
        localStorage.setItem('theme', theme);
        if (theme === 'light') {
            dom.html.classList.remove('dark');
            dom.themeToggleCheckbox.checked = true;
        } else {
            dom.html.classList.add('dark');
            dom.themeToggleCheckbox.checked = false;
        }
    }

    // --- Add app initialization logic ---
    (() => {
        // Load data from local storage
        loadHistory();

        // Setup theme based on saved preference or system setting
        const savedTheme = localStorage.getItem('theme') as 'light' | 'dark' | null;
        applyTheme(savedTheme || 'dark');
        
        // Register Service Worker for PWA functionality
        if ('serviceWorker' in navigator) {
            window.addEventListener('load', () => {
                navigator.serviceWorker.register('sw.js')
                    .then(registration => {
                        console.log('ServiceWorker registration successful with scope: ', registration.scope);
                    })
                    .catch(err => {
                        console.log('ServiceWorker registration failed: ', err);
                    });
            });
        }

        // Perform initial render to reflect loaded state
        render();
    })();
    
    dom.themeToggleCheckbox.addEventListener('change', (e) => {
        applyTheme((e.target as HTMLInputElement).checked ? 'light' : 'dark');
    });

    if (isMainPage) {
        dom.promoBuyCoffee!.addEventListener('click', () => {
            window.open(dom.buyCoffeeBtn!.href, '_blank', 'noopener,noreferrer');
        });
    }

    // --- Coffee Modal Logic ---
    function showCoffeeModal() {
        dom.coffeeModalOverlay?.classList.add('active');
    }

    function hideCoffeeModal() {
        dom.coffeeModalOverlay?.classList.remove('active');
    }
    
    if (dom.coffeeModalCloseBtn) {
        dom.coffeeModalCloseBtn.addEventListener('click', hideCoffeeModal);
    }
    
    if (dom.coffeeModalOverlay) {
        dom.coffeeModalOverlay.addEventListener('click', (e) => {
            if (e.target === dom.coffeeModalOverlay) {
                hideCoffeeModal();
            }
        });
    }

    function checkAndShowCoffeePrompt() {
        const conversionCount = state.history.length;

        const hasShownFinal = localStorage.getItem('hasShownFinalCoffeePrompt') === 'true';
        if (conversionCount >= FINAL_COFFEE_PROMPT_THRESHOLD && !hasShownFinal) {
            showCoffeeModal();
            localStorage.setItem('hasShownFinalCoffeePrompt', 'true');
            return;
        }
        
        const hasShownFirst = localStorage.getItem('hasShownFirstCoffeePrompt') === 'true';
        if (conversionCount >= FIRST_COFFEE_PROMPT_THRESHOLD && !hasShownFirst) {
            showCoffeeModal();
            localStorage.setItem('hasShownFirstCoffeePrompt', 'true');
        }
    }

    function clearProcessingMessageCycle() {
        processingMessageTimeouts.forEach(clearTimeout);
        processingMessageTimeouts = [];
    }
    
    function startProcessingMessageCycle() {
        clearProcessingMessageCycle();
    
        state.processingMessage = 'Analyzing...';
        renderResult();
    
        const t1 = setTimeout(() => {
            if (state.conversionStatus !== 'processing') return;
            state.processingMessage = 'Converting...';
            renderResult();
        }, 2000);
    
        const t2 = setTimeout(() => {
            if (state.conversionStatus !== 'processing') return;
            state.processingMessage = 'Finalizing...';
            renderResult();
        }, 12000); 
    
        processingMessageTimeouts.push(t1, t2);
    }
    
    async function pollUntilDone(jobId: string) {
        if (pollingInterval) {
            clearInterval(pollingInterval);
        }
    
        const POLLING_INTERVAL_MS = 2000;
        const TIMEOUT_MS = 5 * 60 * 1000; // 5 minutes
        const startTime = Date.now();
    
        pollingInterval = window.setInterval(async () => {
            if (Date.now() - startTime > TIMEOUT_MS) {
                clearInterval(pollingInterval!);
                pollingInterval = null;
                state.errorMessage = 'Conversion timed out. Please try again.';
                state.conversionStatus = 'error';
                clearProcessingMessageCycle();
                render();
                return;
            }
    
            try {
                const res = await fetch(`${BASE_URL}/status/${jobId}`);
                if (res.status === 404) {
                    console.warn(`Job ${jobId} not found yet, will retry.`);
                    return; // Don't throw an error, just wait for the next interval
                }
                if (!res.ok) {
                    throw new Error(`Status check failed: ${res.status}`);
                }
                const statusData = await res.json();
    
                if (statusData.status === 'completed' && statusData.download_url) {
                    clearInterval(pollingInterval!);
                    pollingInterval = null;
    
                    if (state.videoInfo) {
                        state.videoInfo.title = statusData.metadata?.title || state.videoInfo.title || 'Untitled';
                        state.videoInfo.duration = statusData.metadata?.duration;
                    }
                    
                    state.downloadUrl = statusData.download_url;
                    state.conversionStatus = 'completed';
    
                    addToHistory({
                        ...(state.videoInfo as Omit<VideoInfo, 'url' | 'conversionDate'>),
                        url: state.currentUrl,
                        conversionDate: new Date().toISOString(),
                    });
                    
                    checkAndShowCoffeePrompt();
                    clearProcessingMessageCycle();
                    render();
    
                } else if (statusData.status === 'failed') {
                    clearInterval(pollingInterval!);
                    pollingInterval = null;
                    state.errorMessage = statusData.error || 'Conversion failed for an unknown reason.';
                    state.conversionStatus = 'error';
                    clearProcessingMessageCycle();
                    render();
                }
            } catch (error: any) {
                clearInterval(pollingInterval!);
                pollingInterval = null;
                state.errorMessage = error.message || 'Failed to check conversion status.';
                state.conversionStatus = 'error';
                clearProcessingMessageCycle();
                render();
            }
        }, POLLING_INTERVAL_MS);
    }
    
    async function startConversion(url: string) {
        clearProcessingMessageCycle();
        if (pollingInterval) clearInterval(pollingInterval);

        // 1. Reset state and enter 'loading'
        state.currentUrl = url;
        state.errorMessage = '';
        state.processingMessage = '';
        state.videoInfo = null;
        state.downloadUrl = null;
        state.currentJobId = null;
        state.conversionStatus = 'loading';
        render();

        // 2. Fetch metadata from noembed.com for immediate UI feedback
        const videoId = extractYouTubeVideoId(url)!;
        try {
            const noembedRes = await fetch(`https://noembed.com/embed?url=${encodeURIComponent(url)}`);
            if (noembedRes.ok) {
                const metadata = await noembedRes.json();
                if (!metadata.error) {
                    state.videoInfo = {
                        id: videoId,
                        title: metadata.title || 'Untitled',
                        thumbnail: metadata.thumbnail_url || `https://i.ytimg.com/vi/${videoId}/hqdefault.jpg`,
                        format: 'MP3',
                    };
                }
            }
        } catch (e) {
            console.warn("Could not fetch metadata from noembed.com, will use fallback.", e);
        }
        
        // Fallback if metadata fetch failed
        if (!state.videoInfo) {
            state.videoInfo = {
                id: videoId,
                title: 'Fetching title...', // This will be updated later
                thumbnail: `https://i.ytimg.com/vi/${videoId}/hqdefault.jpg`,
                format: 'MP3',
            };
        }
        
        // 3. Transition to 'processing' state now that we have some metadata
        state.conversionStatus = 'processing';
        startProcessingMessageCycle();
        render();

        // 4. Start the actual conversion job with our backend
        try {
            const res = await fetch(`${BASE_URL}/extract`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ url: url })
            });

            if (!res.ok) {
                const errorText = await res.text();
                throw new Error(`Server error: ${res.status} - ${errorText || 'Failed to start job.'}`);
            }

            const data = await res.json();
            state.currentJobId = data.job_id;

            // 5. Handle response - either fast path completion or start polling
            if (data.status === 'completed' && data.download_url) {
                // Fast path
                if (state.videoInfo) {
                    state.videoInfo.title = data.metadata?.title || state.videoInfo.title;
                    state.videoInfo.duration = data.metadata?.duration;
                }
                state.downloadUrl = data.download_url;
                state.conversionStatus = 'completed';

                addToHistory({
                    ...(state.videoInfo as Omit<VideoInfo, 'url' | 'conversionDate'>),
                    url: state.currentUrl,
                    conversionDate: new Date().toISOString(),
                });

                checkAndShowCoffeePrompt();
                clearProcessingMessageCycle();
                render();
            } else {
                // Normal path: start polling
                pollUntilDone(data.job_id);
            }

        } catch (error: any) {
            state.errorMessage = error.message || 'Failed to start conversion. Check connection or URL.';
            state.conversionStatus = 'error';
            clearProcessingMessageCycle();
            render();
        }
    }


    if (isMainPage) {
        dom.form!.addEventListener('submit', (e) => {
            e.preventDefault();
            const url = dom.urlInput!.value.trim();
            if (!isValidYouTubeUrl(url)) {
                state.errorMessage = 'Please enter a valid YouTube video URL.';
                render();
                dom.urlInput!.focus();
                return;
            }
            startConversion(url);
        });

        dom.urlInput!.addEventListener('input', () => {
            if (state.errorMessage) {
                state.errorMessage = '';
            }
            renderForm();
        });

        dom.pasteBtn!.addEventListener('click', async () => {
            try {
                const text = await navigator.clipboard.readText();
                if (text) {
                    dom.urlInput!.value = text;
                    dom.urlInput!.dispatchEvent(new Event('input', { bubbles: true }));
                    dom.urlInput!.focus();
                }
            } catch (err) {
                console.error('Failed to read clipboard contents: ', err);
                alert('Could not access clipboard. Please paste manually.');
            }
        });

        dom.clearBtn!.addEventListener('click', () => {
            dom.urlInput!.value = '';
            if (state.errorMessage) {
                state.errorMessage = '';
            }
            dom.urlInput!.dispatchEvent(new Event('input', { bubbles: true }));
            dom.urlInput!.focus();
        });

        dom.urlInput!.addEventListener('blur', () => {
            const url = dom.urlInput!.value;
            if (url && !isValidYouTubeUrl(url)) {
                state.errorMessage = 'The URL format seems incorrect. Please check it.';
            } else if (state.errorMessage) {
                state.errorMessage = '';
            }
            renderForm();
        });
        
        dom.downloadBtn!.addEventListener('click', (e) => {
            const downloadButton = e.currentTarget as HTMLAnchorElement;
            const downloadUrl = downloadButton.href;
    
            if (!downloadUrl || downloadUrl.endsWith('#')) {
                e.preventDefault(); 
                return;
            }
    
            if (downloadButton.dataset.downloading === 'true') {
                e.preventDefault();
                return;
            }
    
            if (!dom.downloadBtnText || !dom.downloadBtnLoader) {
                console.error('Download button text/loader elements not found');
                return;
            }
            
            downloadButton.dataset.downloading = 'true';
            dom.downloadBtnText.textContent = 'Preparing...';
            dom.downloadBtnLoader.classList.remove('hidden');
            downloadButton.classList.add('opacity-75', 'cursor-not-allowed');
    
            setTimeout(() => {
                delete downloadButton.dataset.downloading;
                dom.downloadBtnText.textContent = 'Download';
                dom.downloadBtnLoader.classList.add('hidden');
                downloadButton.classList.remove('opacity-75', 'cursor-not-allowed');
            }, 1500);
        });

        dom.convertAnotherBtn!.addEventListener('click', () => {
            if (state.currentJobId) {
                // Use 'no-cors' mode as a workaround for potential server-side CORS issues
                // with the DELETE method. This sends the request but we cannot read the response.
                // Since this is a non-critical cleanup operation, we "fire and forget".
                fetch(`${BASE_URL}/delete/${state.currentJobId}`, {
                    method: 'DELETE',
                    mode: 'no-cors',
                }).catch(err => {
                    // This will catch fundamental network errors (e.g. server is offline), but not CORS errors.
                    console.warn(`Could not send delete request for job ${state.currentJobId}. It will auto-expire.`, err);
                });
            }

            if (pollingInterval) clearInterval(pollingInterval);
            clearProcessingMessageCycle();
            state.conversionStatus = 'idle';
            state.videoInfo = null;
            state.errorMessage = '';
            state.downloadUrl = null;
            state.currentJobId = null;
            dom.urlInput!.value = '';
            
            const downloadButton = dom.downloadBtn!;
            downloadButton.href = '#';
            downloadButton.title = '';
            downloadButton.removeAttribute('download');
            delete downloadButton.dataset.hasAnimated;
            
            delete downloadButton.dataset.downloading;
            if (dom.downloadBtnText) dom.downloadBtnText.textContent = 'Download';
            if (dom.downloadBtnLoader) dom.downloadBtnLoader.classList.add('hidden');
            downloadButton.classList.remove('opacity-75', 'cursor-not-allowed');

            render();
            requestAnimationFrame(() => dom.urlInput!.focus());
        });
    }

    dom.historyToggleBtn.addEventListener('click', () => {
        dom.historyModalOverlay.classList.add('active');
    });

    dom.closeHistoryModalBtn.addEventListener('click', () => {
        dom.historyModalOverlay.classList.remove('active');
    });

    dom.historyModalOverlay.addEventListener('click', (e) => {
        if (e.target === dom.historyModalOverlay) {
            dom.historyModalOverlay.classList.remove('active');
        }
    });

    dom.clearHistoryBtn.addEventListener('click', () => {
        if (confirm('Are you sure you want to clear your entire conversion history? This cannot be undone.')) {
            state.history = [];
            saveHistory();
            renderHistory();
        }
    });

    // Use event delegation for dynamically created history items
    dom.historyContainer.addEventListener('click', (e) => {
        const target = e.target as HTMLElement;

        const deleteButton = target.closest('.delete-history-btn');
        if (deleteButton && deleteButton.getAttribute('data-history-id')) {
            const historyId = deleteButton.getAttribute('data-history-id')!;
            state.history = state.history.filter(item => item.id !== historyId);
            saveHistory();
            renderHistory();
            return;
        }

        const regenerateButton = target.closest('.regenerate-btn');
        if (regenerateButton && regenerateButton.getAttribute('data-history-id')) {
            if (!isMainPage) {
                alert('Conversions can only be started from the main page.');
                return;
            }
            const historyId = regenerateButton.getAttribute('data-history-id')!;
            const itemToRegenerate = state.history.find(item => item.id === historyId);
            if (itemToRegenerate && itemToRegenerate.url) {
                dom.historyModalOverlay.classList.remove('active');
                dom.urlInput!.value = itemToRegenerate.url;
                dom.urlInput!.dispatchEvent(new Event('input', { bubbles: true }));
                startConversion(itemToRegenerate.url);
            }
        }
    });

    dom.shortcutsToggleBtn.addEventListener('click', () => {
        dom.shortcutsModalOverlay.classList.add('active');
    });

    dom.closeShortcutsModalBtn.addEventListener('click', () => {
        dom.shortcutsModalOverlay.classList.remove('active');
    });
    
    dom.shortcutsModalOverlay.addEventListener('click', (e) => {
        if (e.target === dom.shortcutsModalOverlay) {
            dom.shortcutsModalOverlay.classList.remove('active');
        }
    });

    // Keyboard shortcuts
    document.addEventListener('keydown', (e) => {
        const isModalOpen = dom.historyModalOverlay.classList.contains('active') ||
                            dom.shortcutsModalOverlay.classList.contains('active') ||
                            dom.coffeeModalOverlay?.classList.contains('active');
        
        const activeEl = document.activeElement;
        const isTyping = activeEl && (activeEl.tagName === 'INPUT' || activeEl.tagName === 'TEXTAREA' || (activeEl as HTMLElement).isContentEditable);
    
        if (e.key === 'Escape') {
            if (isModalOpen) {
                e.preventDefault();
                dom.historyModalOverlay.classList.remove('active');
                dom.shortcutsModalOverlay.classList.remove('active');
                dom.coffeeModalOverlay?.classList.remove('active');
            }
            return;
        }
    
        if (isModalOpen) return;
        if (isTyping && !(activeEl === dom.urlInput && e.key === 'Enter')) return;
    
        const key = e.key.toLowerCase();
    
        if (e.altKey && key === 'u') {
            if (!isMainPage) return;
            e.preventDefault();
            dom.urlInput?.focus();
        }
    
        else if (e.altKey && key === 'd') {
            if (isMainPage && state.conversionStatus === 'completed' && dom.downloadBtn) {
                e.preventDefault();
                dom.downloadBtn.click();
            }
        }
    
        else if (e.altKey && key === 'n') {
            if (isMainPage && state.conversionStatus === 'completed' && dom.convertAnotherBtn) {
                e.preventDefault();
                dom.convertAnotherBtn.click();
            }
        }
    
        else if (e.altKey && key === 'h') {
            e.preventDefault();
            dom.historyToggleBtn?.click();
        }
    
        else if (e.altKey && key === 'c') {
            if (isMainPage && dom.copyLinkBtn) {
                e.preventDefault();
                dom.copyLinkBtn.click();
            }
        }
    
        else if ((e.ctrlKey || e.metaKey) && key === 'v') {
            if (isMainPage && dom.urlInput && activeEl !== dom.urlInput) {
                dom.urlInput.focus();
            }
        }
    });

    // Welcome screen logic
    if (isMainPage && dom.welcomeOverlay && !localStorage.getItem('hasSeenWelcome')) {
        dom.welcomeOverlay.classList.add('active');
        dom.welcomeStartBtn!.addEventListener('click', () => {
            dom.welcomeCard!.classList.add('fade-out');
            dom.welcomeCard!.addEventListener('animationend', () => {
                dom.welcomeOverlay.classList.remove('active');
                localStorage.setItem('hasSeenWelcome', 'true');
            }, { once: true });
        });
    }

    // FAQ Accordion
    if (dom.faqContainer) {
        dom.faqContainer.addEventListener('click', (e) => {
            const button = (e.target as HTMLElement).closest('[data-faq-question]');
            if (button) {
                const answer = button.nextElementSibling as HTMLElement;
                const icon = button.querySelector('svg');
                const isOpening = !button.classList.contains('active');

                dom.faqContainer!.querySelectorAll('[data-faq-question]').forEach(btn => {
                    if (btn !== button) {
                        btn.classList.remove('active');
                        (btn.nextElementSibling as HTMLElement).style.maxHeight = '';
                        btn.querySelector('svg')?.classList.remove('rotate-180');
                    }
                });

                button.classList.toggle('active', isOpening);
                icon?.classList.toggle('rotate-180', isOpening);
                answer.style.maxHeight = isOpening ? `${answer.scrollHeight}px` : '';
            }
        });
    }
});
