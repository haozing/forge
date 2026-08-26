package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type report struct {
	URL          string  `json:"url"`
	Requests     int     `json:"requests"`
	Concurrency  int     `json:"concurrency"`
	Failures     int64   `json:"failures"`
	ErrorRate    float64 `json:"error_rate"`
	DurationMS   float64 `json:"duration_ms"`
	RequestsPerS float64 `json:"requests_per_second"`
	P50MS        float64 `json:"p50_ms"`
	P95MS        float64 `json:"p95_ms"`
	P99MS        float64 `json:"p99_ms"`
}

func main() {
	url := flag.String("url", "http://127.0.0.1:8080/healthz", "HTTP endpoint to exercise")
	requests := flag.Int("requests", 1000, "total requests")
	concurrency := flag.Int("concurrency", 25, "concurrent workers")
	requestTimeout := flag.Duration("timeout", 10*time.Second, "per-request timeout")
	maxP95 := flag.Duration("max-p95", 500*time.Millisecond, "maximum accepted p95")
	maxErrorRate := flag.Float64("max-error-rate", 0, "maximum accepted error ratio")
	flag.Parse()
	if *requests < 1 || *concurrency < 1 || *concurrency > *requests || *maxErrorRate < 0 || *maxErrorRate > 1 {
		fmt.Fprintln(os.Stderr, "invalid load-test arguments")
		os.Exit(1)
	}

	client := &http.Client{Timeout: *requestTimeout}
	jobs := make(chan struct{})
	latencies := make([]time.Duration, 0, *requests)
	var latencyMu sync.Mutex
	var failures atomic.Int64
	var workers sync.WaitGroup
	started := time.Now()
	for range *concurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range jobs {
				requestStarted := time.Now()
				req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, *url, nil)
				if err == nil {
					var response *http.Response
					response, err = client.Do(req)
					if response != nil {
						_, _ = io.Copy(io.Discard, response.Body)
						_ = response.Body.Close()
						if response.StatusCode < 200 || response.StatusCode >= 300 {
							err = fmt.Errorf("unexpected status %d", response.StatusCode)
						}
					}
				}
				if err != nil {
					failures.Add(1)
				}
				latencyMu.Lock()
				latencies = append(latencies, time.Since(requestStarted))
				latencyMu.Unlock()
			}
		}()
	}
	for range *requests {
		jobs <- struct{}{}
	}
	close(jobs)
	workers.Wait()
	duration := time.Since(started)
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	failureCount := failures.Load()
	result := report{
		URL:          *url,
		Requests:     *requests,
		Concurrency:  *concurrency,
		Failures:     failureCount,
		ErrorRate:    float64(failureCount) / float64(*requests),
		DurationMS:   milliseconds(duration),
		RequestsPerS: float64(*requests) / duration.Seconds(),
		P50MS:        milliseconds(percentile(latencies, 0.50)),
		P95MS:        milliseconds(percentile(latencies, 0.95)),
		P99MS:        milliseconds(percentile(latencies, 0.99)),
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(result)
	if result.ErrorRate > *maxErrorRate || percentile(latencies, 0.95) > *maxP95 {
		os.Exit(2)
	}
}

func percentile(values []time.Duration, fraction float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	index := int(float64(len(values)-1) * fraction)
	return values[index]
}

func milliseconds(value time.Duration) float64 {
	return float64(value.Microseconds()) / 1000
}
