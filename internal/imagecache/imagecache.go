package imagecache

import (
	"crypto/tls"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

type Cache struct {
	mu        sync.RWMutex
	bitmaps   map[string]*ui.Bitmap
	inFlight  map[string]bool
	failed    map[string]bool
	client    *http.Client
	onUpdated func(url string, bmp *ui.Bitmap)
}

func New(onUpdated func(url string, bmp *ui.Bitmap)) *Cache {
	return &Cache{
		bitmaps:   make(map[string]*ui.Bitmap),
		inFlight:  make(map[string]bool),
		failed:    make(map[string]bool),
		onUpdated: onUpdated,
		client: &http.Client{
			Timeout: 12 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // ignore invalid certs on CDN
				MaxIdleConns:    20,
			},
		},
	}
}

// Get returns the cached bitmap if available. If not, it triggers background download.
func (c *Cache) Get(url string) *ui.Bitmap {
	if url == "" {
		return nil
	}

	c.mu.RLock()
	bmp, exists := c.bitmaps[url]
	failed := c.failed[url]
	c.mu.RUnlock()

	if exists {
		return bmp
	}
	if failed {
		return nil
	}

	c.mu.Lock()
	if c.inFlight[url] {
		c.mu.Unlock()
		return nil
	}
	c.inFlight[url] = true
	c.mu.Unlock()

	go c.fetch(url)
	return nil
}

func (c *Cache) fetch(url string) {
	defer func() {
		c.mu.Lock()
		delete(c.inFlight, url)
		c.mu.Unlock()
	}()

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		c.markFailed(url)
		return
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Referer", url)

	resp, err := c.client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		c.markFailed(url)
		return
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil || len(data) == 0 {
		c.markFailed(url)
		return
	}

	bmp, err := ui.DecodeBitmap(data)
	if err != nil {
		c.markFailed(url)
		return
	}

	c.mu.Lock()
	c.bitmaps[url] = bmp
	c.mu.Unlock()

	if c.onUpdated != nil {
		c.onUpdated(url, bmp)
	}
}

func (c *Cache) markFailed(url string) {
	c.mu.Lock()
	c.failed[url] = true
	c.mu.Unlock()
}
