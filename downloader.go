package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const INDEX_FILE_NAME = "index.m3u8"

var bufPool = sync.Pool{
	New: func() interface{} {
		return new(bytes.Buffer)
	},
}

type CachedResult struct {
	downloadJob
	Data        io.WriterTo
	ContentType string
}

type downloadJob struct {
	index int
	url   string
}

func (r *CachedResult) Close() error {
	bufPool.Put(r.Data)
	r.Data = nil

	return nil
}

type Downloader struct {
	client   *http.Client
	baseUrl  *url.URL
	buffered map[string]CachedResult

	cond            *sync.Cond
	downloadedCount int32
	totalCount      int
	inputChan       chan downloadJob
}

func NewDownloader(url string) *Downloader {
	downloader := &Downloader{
		client:    &http.Client{},
		cond:      sync.NewCond(new(sync.Mutex)),
		buffered:  make(map[string]CachedResult),
		inputChan: make(chan downloadJob),
	}

	go downloader.download(downloadJob{
		index: 0,
		url:   url,
	}, true)

	for i := 0; i < *workerNum; i++ {
		go downloader.worker()
	}

	return downloader
}

func (d *Downloader) worker() {
	for job := range d.inputChan {
		d.download(job, false)
	}
}

func (d *Downloader) putResult(result CachedResult) {
	d.cond.L.Lock()
	defer d.cond.L.Unlock()

	for (int(d.downloadedCount) + *chunkNum) < result.index {
		d.cond.Wait()
	}

	d.buffered[result.url] = result
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

func (d *Downloader) onDownloadFailed(job downloadJob, err error) {
	fmt.Printf("Download '%v' failed, %s", job, err.Error())
	time.Sleep(time.Second * 3)

	d.inputChan <- job
}

func (d *Downloader) download(job downloadJob, isInitUrl bool) {
	var normalizedUrl string
	if isInitUrl {
		normalizedUrl = job.url
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
		panic(fmt.Errorf("download '%v' failed, status code: %d", job, resp.StatusCode))
	}

	contentType := resp.Header.Get("Content-Type")

	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()

	if _, err := buf.ReadFrom(resp.Body); err != nil {
		d.onDownloadFailed(job, err)
		return
	}

	result := CachedResult{
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

	d.baseUrl = req.URL
	d.totalCount = len(urls) + 1

	result.url = INDEX_FILE_NAME
	d.putResult(result)

	for i, url := range urls {
		d.inputChan <- downloadJob{
			index: i + 1,
			url:   url,
		}
	}

	close(d.inputChan)
}

// Get the data of the url. will wait unitl the data is ready.
func (d *Downloader) GetResult(url string) CachedResult {
	d.cond.L.Lock()
	defer d.cond.L.Unlock()

	for {
		data, ok := d.buffered[url]
		if ok {
			delete(d.buffered, url)
			d.downloadedCount++
			d.cond.Broadcast()

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
