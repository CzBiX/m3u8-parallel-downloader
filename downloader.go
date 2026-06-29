package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"
)

const INDEX_FILE_NAME = "index.m3u8"

var bufPool = sync.Pool{
	New: func() any {
		return bytes.NewBuffer(make([]byte, 0, 64*1024))
	},
}

type Chunk struct {
	ContentType string
	Data        *bytes.Buffer
}

func (c *Chunk) Close() {
	if c.Data != nil {
		bufPool.Put(c.Data)
		c.Data = nil
	}
}

type Downloader struct {
	baseUrl  string
	capacity int

	urls       []string
	urlToIndex map[string]int

	cache    map[int]*Chunk
	order    []int
	inflight map[int]bool

	indexParsed bool
	errs        map[int]error
	retries     map[int]int
	closed      bool

	mu   sync.Mutex
	cond *sync.Cond
	wg   sync.WaitGroup

	jobs chan int

	ctx    context.Context
	cancel context.CancelFunc
}

const maxRetries = 5

func NewDownloader(baseUrl string, capacity int, workerNum int) *Downloader {
	ctx, cancel := context.WithCancel(context.Background())
	d := &Downloader{
		baseUrl:    baseUrl,
		capacity:   capacity,
		urlToIndex: make(map[string]int),
		cache:      make(map[int]*Chunk),
		inflight:   make(map[int]bool),
		errs:       make(map[int]error),
		retries:    make(map[int]int),
		jobs:       make(chan int, capacity),
		ctx:        ctx,
		cancel:     cancel,
	}
	d.cond = sync.NewCond(&d.mu)

	d.urls = []string{INDEX_FILE_NAME}
	d.urlToIndex[INDEX_FILE_NAME] = 0

	d.wg.Add(workerNum)
	for range workerNum {
		go d.worker()
	}

	d.inflight[0] = true
	d.jobs <- 0

	return d
}

func (d *Downloader) worker() {
	defer d.wg.Done()
	for {
		select {
		case <-d.ctx.Done():
			return
		case idx := <-d.jobs:
			if err := d.download(idx); err != nil {
				fmt.Printf("download %d failed: %s\n", idx, err.Error())
				d.mu.Lock()
				d.retries[idx]++
				if d.retries[idx] >= maxRetries {
					d.errs[idx] = err
					delete(d.inflight, idx)
					d.mu.Unlock()
					d.cond.Broadcast()
					continue
				}
				d.mu.Unlock()
				select {
				case <-d.ctx.Done():
					return
				case <-time.After(3 * time.Second):
				}
				d.mu.Lock()
				d.inflight[idx] = true
				d.mu.Unlock()
				d.submitJob(idx)
				d.cond.Broadcast()
				continue
			}
			d.cond.Broadcast()
		}
	}
}

func (d *Downloader) submitJob(idx int) {
	select {
	case d.jobs <- idx:
	default:
		go func() {
			select {
			case d.jobs <- idx:
			case <-d.ctx.Done():
			}
		}()
	}
}

func (d *Downloader) download(idx int) error {
	// Read URL under lock
	d.mu.Lock()
	var fullURL string
	if idx == 0 {
		fullURL = d.baseUrl
	} else {
		if idx >= len(d.urls) {
			d.mu.Unlock()
			return fmt.Errorf("index %d out of range (len=%d)", idx, len(d.urls))
		}
		fullURL = d.urls[idx]
	}
	d.mu.Unlock()

	// Build full URL for relative paths
	if idx > 0 {
		base, err := url.Parse(d.baseUrl)
		if err != nil {
			return fmt.Errorf("parse base url: %w", err)
		}
		ref, err := url.Parse(fullURL)
		if err != nil {
			return fmt.Errorf("parse ref url: %w", err)
		}
		fullURL = base.ResolveReference(ref).String()
	}

	// HTTP GET
	req, err := http.NewRequestWithContext(d.ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", ua)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http status: %d", resp.StatusCode)
	}

	// Read response to buffer
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	if _, err := io.Copy(buf, resp.Body); err != nil {
		bufPool.Put(buf)
		return fmt.Errorf("read response: %w", err)
	}

	contentType := resp.Header.Get("Content-Type")

	// If index=0, parse m3u8 and fill urls/urlToIndex under lock
	if idx == 0 {
		parsedUrls := ParseM3U8Urls(buf)
		if parsedUrls == nil {
			bufPool.Put(buf)
			return fmt.Errorf("parse m3u8 failed")
		}

		d.mu.Lock()
		// Reset urls/urlToIndex to avoid stale entries (fixes #8)
		d.urls = []string{INDEX_FILE_NAME}
		d.urlToIndex = make(map[string]int)
		d.urlToIndex[INDEX_FILE_NAME] = 0
		for _, u := range parsedUrls {
			if _, exists := d.urlToIndex[u]; !exists {
				d.urlToIndex[u] = len(d.urls)
				d.urls = append(d.urls, u)
			}
		}
		d.indexParsed = true
		d.mu.Unlock()
	}

	// Put into cache
	d.putChunk(idx, &Chunk{
		ContentType: contentType,
		Data:        buf,
	})

	return nil
}

func (d *Downloader) putChunk(idx int, chunk *Chunk) {
	d.mu.Lock()

	if d.closed {
		d.mu.Unlock()
		chunk.Close()
		return
	}

	// If a duplicate download produced a chunk for an idx already cached,
	// close the old one and reuse the new (fixes #3 buffer leak on overwrite).
	if old, ok := d.cache[idx]; ok {
		old.Close()
		delete(d.cache, idx)
		d.removeFromOrderLocked(idx)
	}

	// Evict if full
	if len(d.cache) >= d.capacity && d.capacity > 0 {
		for len(d.order) > 0 {
			oldest := d.order[0]
			d.order = d.order[1:]
			if evicted, ok := d.cache[oldest]; ok {
				evicted.Close()
				delete(d.cache, oldest)
				break
			}
		}
		// Compact order backing array if shrunk significantly (fixes #9)
		if cap(d.order) > 2*len(d.order)+8 {
			newOrder := make([]int, len(d.order))
			copy(newOrder, d.order)
			d.order = newOrder
		}
	}

	d.cache[idx] = chunk
	d.order = append(d.order, idx)

	delete(d.inflight, idx)
	delete(d.errs, idx)
	delete(d.retries, idx)

	cached := len(d.cache)
	inflight := len(d.inflight)
	total := len(d.urls)
	orderCopy := append([]int(nil), d.order...)
	inflightIdxs := make([]int, 0, inflight)
	for k := range d.inflight {
		inflightIdxs = append(inflightIdxs, k)
	}
	d.mu.Unlock()

	slices.Sort(inflightIdxs)
	fmt.Printf("\033[2K\r[stats] cached=%d/%d %v | inflight=%d %v | total=%d",
		cached, d.capacity, orderCopy, inflight, inflightIdxs, total)
}

func (d *Downloader) removeFromOrderLocked(idx int) {
	for i, v := range d.order {
		if v == idx {
			d.order = append(d.order[:i], d.order[i+1:]...)
			return
		}
	}
}

func (d *Downloader) Get(path string) (*Chunk, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Find index for path, waiting until the m3u8 is parsed if necessary.
	idx, exists := d.urlToIndex[path]
	for !exists {
		if d.errs[0] != nil {
			return nil, fmt.Errorf("index download failed: %w", d.errs[0])
		}
		if d.indexParsed {
			return nil, fmt.Errorf("unknown path: %s", path)
		}
		if d.ctx.Err() != nil {
			return nil, fmt.Errorf("downloader closed")
		}
		d.cond.Wait()
		idx, exists = d.urlToIndex[path]
	}

	for {
		// Check cancellation (fixes #2)
		if d.ctx.Err() != nil {
			return nil, fmt.Errorf("downloader closed")
		}

		// Surface terminal download errors for this index
		if err, ok := d.errs[idx]; ok {
			return nil, fmt.Errorf("download %d failed: %w", idx, err)
		}

		// Check cache
		if chunk, ok := d.cache[idx]; ok {
			delete(d.cache, idx)
			d.removeFromOrderLocked(idx)
			go d.prefetch(idx + 1)
			return chunk, nil
		}

		// Submit download if not already in flight (fixes #3 infight dedup)
		if !d.inflight[idx] {
			d.inflight[idx] = true
			d.submitJob(idx)
		}

		// Wait for notification
		d.cond.Wait()
	}
}

func (d *Downloader) prefetch(from int) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if len(d.urls) <= 1 {
		return
	}

	for i := range d.capacity {
		idx := from + i
		if idx >= len(d.urls) {
			break
		}

		if _, ok := d.cache[idx]; ok {
			continue
		}
		if d.inflight[idx] {
			continue
		}
		if _, ok := d.errs[idx]; ok {
			continue
		}

		d.inflight[idx] = true
		d.submitJob(idx)
	}
}

func (d *Downloader) Close() {
	d.cancel()
	d.mu.Lock()
	d.closed = true
	d.cond.Broadcast()
	for _, r := range d.cache {
		r.Close()
	}
	d.cache = nil
	d.mu.Unlock()
	d.wg.Wait()
}
