package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

const INDEX_FILE_NAME = "index.m3u8"

var bufPool = sync.Pool{
	New: func() interface{} {
		return new(bytes.Buffer)
	},
}

type CachedResult struct {
	Data        io.WriterTo
	ContentType string
}

func (r *CachedResult) Close() error {
	bufPool.Put(r.Data.(*bytes.Buffer))

	return nil
}

type Downloader struct {
	client   *http.Client
	baseUrl  *url.URL
	buffered map[string]CachedResult

	cond            *sync.Cond
	downloadedCount int32
	totalCount      int
	inputChan       chan string
}

func NewDownloader(url string) *Downloader {
	downloader := &Downloader{
		client:    &http.Client{},
		cond:      sync.NewCond(new(sync.Mutex)),
		buffered:  make(map[string]CachedResult),
		inputChan: make(chan string),
	}

	go downloader.download(url, true)

	for i := 0; i < *workerNum; i++ {
		go downloader.worker()
	}

	return downloader
}

func (d *Downloader) worker() {
	for url := range d.inputChan {
		d.download(url, false)
	}
}

func (d *Downloader) putResult(url string, result CachedResult) {
	d.cond.L.Lock()
	defer d.cond.L.Unlock()

	for len(d.buffered) >= *chunkNum {
		d.cond.Wait()
	}

	d.buffered[url] = result
	d.cond.Broadcast()

	d.printStatus()
}

func (d *Downloader) normalizeUrl(inputUrl string) string {
	u, err := url.Parse(inputUrl)
	if err != nil {
		panic(fmt.Sprintf("parse url error: %s", err.Error()))
	}

	u = d.baseUrl.ResolveReference(u)

	return u.String()
}

func (d *Downloader) onDownloadFailed(url string, err error) {
	fmt.Printf("Download '%s' failed, %s", url, err.Error())
	time.Sleep(time.Second * 3)

	d.inputChan <- url
}

func (d *Downloader) download(inputUrl string, isInitUrl bool) {
	var normalizedUrl string
	if isInitUrl {
		normalizedUrl = inputUrl
	} else {
		normalizedUrl = d.normalizeUrl(inputUrl)
	}

	req, err := http.NewRequest("GET", normalizedUrl, nil)
	if err != nil {
		panic(fmt.Sprintf("create request error: %s", err.Error()))
	}

	req.Header.Set("User-Agent", ua)

	resp, err := d.client.Do(req)
	if err != nil {
		d.onDownloadFailed(inputUrl, err)
		return
	}

	if resp.StatusCode != http.StatusOK {
		panic(fmt.Sprintf("download '%s' failed, status code: %d", inputUrl, resp.StatusCode))
	}

	contentType := resp.Header.Get("Content-Type")

	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()

	if _, err := buf.ReadFrom(resp.Body); err != nil {
		d.onDownloadFailed(inputUrl, err)
		return
	}

	result := CachedResult{
		Data:        buf,
		ContentType: contentType,
	}

	if !isInitUrl {
		d.putResult(inputUrl, result)
		return
	}

	urls := ParseM3U8Urls(buf)
	fmt.Printf("%d urls found\n", len(urls))

	d.baseUrl = req.URL
	d.totalCount = len(urls) + 1

	d.putResult(INDEX_FILE_NAME, result)

	for _, url := range urls {
		d.inputChan <- url
	}
}

// Get the data of the url. will wait unitl the data is ready.
func (d *Downloader) GetResult(url string) CachedResult {
	d.cond.L.Lock()
	defer d.cond.L.Unlock()

	for {
		data, ok := d.buffered[url]
		if ok {
			delete(d.buffered, url)
			atomic.AddInt32(&d.downloadedCount, 1)
			d.cond.Signal()

			d.printStatus()
			return data
		}

		d.cond.Wait()
	}
}

func (d *Downloader) printStatus() {
	progress := float32(d.downloadedCount) / float32(d.totalCount) * 100
	fmt.Printf("\033[2K\rprogress: %0.2f%%, downloaded: %d, buffered: %d", progress, d.downloadedCount, len(d.buffered))
}
