package main

import (
	"bytes"
	"container/list"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const INDEX_FILE_NAME = "index.m3u8"

var bufPool = sync.Pool{
	New: func() any {
		return new(bytes.Buffer)
	},
}

type CachedResult struct {
	downloadJob
	Data        *bytes.Buffer
	ContentType string
}

type downloadJob struct {
	index int
	url   string
}

func (r *CachedResult) Close() error {
	if r.Data != nil {
		buf := r.Data
		r.Data = nil
		bufPool.Put(buf)
	}
	return nil
}

type Downloader struct {
	client  *http.Client
	baseUrl *url.URL

	urls       []string
	urlToIndex map[string]int

	capacity int
	workers  int

	jobs chan downloadJob

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	cond      *sync.Cond
	buffer    map[int]*CachedResult // cached buffers
	order     *list.List            // order list: front=oldest, back=newest
	infight   map[int]bool          // which indexes are currently being downloaded
	lastIndex int
}

func NewDownloader(startUrl string, capacity, workers int) *Downloader {
	ctx, cancel := context.WithCancel(context.Background())
	d := &Downloader{
		client:     &http.Client{},
		urls:       make([]string, 0),
		urlToIndex: make(map[string]int),
		capacity:   capacity,
		workers:    workers,
		jobs:       make(chan downloadJob, workers*2),
		ctx:        ctx,
		cancel:     cancel,
		buffer:     make(map[int]*CachedResult),
		order:      list.New(),
		lastIndex:  -1,
		infight:    make(map[int]bool),
		cond:       sync.NewCond(new(sync.Mutex)),
	}

	d.baseUrl, _ = url.Parse(startUrl)
	d.pushJob(downloadJob{index: 0, url: startUrl})

	d.startWorkers()

	return d
}

func (d *Downloader) pushJob(job downloadJob) {
	select {
	case d.jobs <- job:
	default:
		go func() { d.jobs <- job }()
	}
}

func (d *Downloader) startWorkers() {
	for i := 0; i < d.workers; i++ {
		d.wg.Add(1)
		go d.worker()
	}
}

func (d *Downloader) worker() {
	defer d.wg.Done()
	for {
		select {
		case <-d.ctx.Done():
			return
		case job := <-d.jobs:
			d.download(job)
		}
	}
}

func (d *Downloader) normalizeUrl(inputUrl string) string {
	u, err := url.Parse(inputUrl)
	if err != nil {
		panic(fmt.Sprintf("parse url error: %s", err.Error()))
	}

	u = d.baseUrl.ResolveReference(u)

	return u.String()
}

func (d *Downloader) putResult(result *CachedResult) {
	d.cond.L.Lock()
	defer d.cond.L.Unlock()

	// evict oldest if needed
	if len(d.buffer) >= d.capacity {
		e := d.order.Front()
		oldIdx := e.Value.(int)
		if old, ok2 := d.buffer[oldIdx]; ok2 {
			_ = old.Close()
		}
		delete(d.buffer, oldIdx)
		d.order.Remove(e)
	}

	d.buffer[result.index] = result
	d.order.PushBack(result.index)

	// mark infight false and notify waiters
	delete(d.infight, result.index)
	d.cond.Broadcast()

	d.printStatus()
}

func (d *Downloader) onDownloadFailed(job downloadJob, err error) {
	fmt.Printf("Download '%v' failed, %s", job, err.Error())
	time.Sleep(time.Second * 3)

	d.jobs <- job
}

// download is a stubbed downloader; replace with real IO.
func (d *Downloader) download(job downloadJob) {
	var normalizedUrl string
	isInitUrl := job.index == 0
	if isInitUrl {
		normalizedUrl = d.baseUrl.String()
	} else {
		normalizedUrl = d.normalizeUrl(job.url)
	}

	req, err := http.NewRequest("GET", normalizedUrl, nil)
	if err != nil {
		panic(fmt.Errorf("create request error: %w", err))
	}

	req.Header.Set("User-Agent", ua)

	resp, err := d.client.Do(req)
	if err != nil {
		d.onDownloadFailed(job, err)
		return
	}

	if resp.StatusCode != http.StatusOK {
		fmt.Printf("Download '%v' failed, status code: %d", job, resp.StatusCode)
		if data, err := io.ReadAll(resp.Body); err == nil {
			fmt.Printf("Content: %s", data)
		}
		panic("Download failed")
	}

	contentType := resp.Header.Get("Content-Type")

	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()

	buf.ReadFrom(resp.Body)

	if _, err = io.Copy(buf, resp.Body); err != nil {
		bufPool.Put(buf)
		d.onDownloadFailed(job, err)
		return
	}

	result := &CachedResult{
		downloadJob: job,
		Data:        buf,
		ContentType: contentType,
	}

	if !isInitUrl {
		d.putResult(result)
		return
	}

	urls := ParseM3U8Urls(buf)
	fmt.Printf("%d urls found\n", len(urls))

	d.urls = append([]string{INDEX_FILE_NAME}, urls...)
	for i, u := range d.urls {
		d.urlToIndex[u] = i
	}

	result.url = INDEX_FILE_NAME
	d.putResult(result)
}

// Get blocks until the requested chunk is available and returns it.
// After returning, the chunk is removed from internal buffer; caller must call Close().
func (d *Downloader) Get(url string) (*CachedResult, error) {
	d.cond.L.Lock()
	defer d.cond.L.Unlock()

	idx, ok := d.urlToIndex[url]
	if !ok {
		return nil, errors.New("url not in manifest")
	}

	for {
		// check cancellation
		if d.ctx.Err() != nil {
			return nil, errors.New("downloader closed")
		}

		// 1) if in buffer, take it and return
		if res, ok := d.buffer[idx]; ok {
			if idx != 0 {
				d.schedulePrefetchLocked(idx+1, d.capacity)
			}
			d.removeFromBufferLocked(idx)

			d.lastIndex = idx
			d.printStatus()
			return res, nil
		}

		// 2) not in buffer: if not infight, mark infight and submit a job
		if !d.infight[idx] {
			d.infight[idx] = true
			d.pushJob(downloadJob{index: idx, url: d.urls[idx]})
		}

		// 3) wait for notification and loop
		d.cond.Wait()
	}
}

// schedulePrefetchLocked enqueues downloads for [from..to] inclusive.
// caller MUST hold d.cond.L.
func (d *Downloader) schedulePrefetchLocked(from, size int) {
	if from >= len(d.urls) {
		return
	}
	to := from + size - 1
	if to >= len(d.urls) {
		to = len(d.urls) - 1
	}

	for i := from; i <= to; i++ {
		if _, inBuf := d.buffer[i]; inBuf {
			continue
		}
		if d.infight[i] {
			continue
		}
		d.infight[i] = true
		d.pushJob(downloadJob{
			index: i,
			url:   d.urls[i],
		})
	}
}

// removeFromBufferLocked removes index from buffer and order. caller must hold d.cond.L.
func (d *Downloader) removeFromBufferLocked(idx int) {
	if _, ok := d.buffer[idx]; !ok {
		return
	}
	for e := d.order.Front(); e != nil; e = e.Next() {
		if e.Value.(int) == idx {
			d.order.Remove(e)
			break
		}
	}
	delete(d.buffer, idx)
}

// Close shuts down the downloader and releases resources.
func (d *Downloader) Close() {
	d.cancel()
	// wake all waiters
	d.cond.L.Lock()
	d.cond.Broadcast()
	// cleanup buffer
	for _, r := range d.buffer {
		_ = r.Close()
	}

	d.buffer = nil
	d.cond.L.Unlock()
	d.wg.Wait()
}

func (d *Downloader) printStatus() {
	bufSize := len(d.buffer)

	fmt.Printf("\033[2K\rstatus: %d/%d, %d buffered", d.lastIndex, len(d.urls), bufSize)
}
