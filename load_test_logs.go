package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
)

const (
	maxRequests       = 1_000_000
	maxRequestsPerSec = 100_000_000
	maxConcurrency    = maxRequests
	defaultEndpoint   = "https://localhost/logs"
)

type result struct {
	status int
	err    error
}

func main() {
	requests := flag.Int("requests", 10, fmt.Sprintf("number of requests (maximum %d)", maxRequests))
	rate := flag.Int("rate", 5, fmt.Sprintf("target requests per second (maximum %d)", maxRequestsPerSec))
	concurrency := flag.Int("concurrency", 5, fmt.Sprintf("parallel requests (maximum %d)", maxConcurrency))
	flag.Parse()

	apiKey := os.Getenv("LOG_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "set LOG_API_KEY in the environment")
		os.Exit(2)
	}
	if *requests < 1 || *requests > maxRequests {
		fmt.Fprintf(os.Stderr, "-requests must be between 1 and %d\n", maxRequests)
		os.Exit(2)
	}
	if *rate < 1 || *rate > maxRequestsPerSec {
		fmt.Fprintf(os.Stderr, "-rate must be between 1 and %d\n", maxRequestsPerSec)
		os.Exit(2)
	}
	if *concurrency < 1 || *concurrency > maxConcurrency {
		fmt.Fprintf(os.Stderr, "-concurrency must be between 1 and %d\n", maxConcurrency)
		os.Exit(2)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	ticker := time.NewTicker(time.Second / time.Duration(*rate))
	defer ticker.Stop()

	slots := make(chan struct{}, *concurrency)
	results := make(chan result, *concurrency)
	endpoint := os.Getenv("LOGSERVICE_URL")
	if endpoint == "" {
		endpoint = defaultEndpoint
	}

	go func() {
		sendRequests(client, endpoint, apiKey, *requests, ticker.C, slots, results)
		close(results)
	}()

	if !monitorResults(os.Stdout, results, *requests) {
		os.Exit(1)
	}
}

func sendRequests(client *http.Client, endpoint, apiKey string, requests int, ticks <-chan time.Time, slots chan struct{}, results chan<- result) {
	var workers sync.WaitGroup

	for i := 0; i < requests; i++ {
		<-ticks
		slots <- struct{}{}
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			defer func() { <-slots }()

			payload, err := json.Marshal(struct {
				Message string `json:"message"`
			}{Message: fmt.Sprintf("local load test %d", index+1)})
			if err != nil {
				results <- result{err: err}
				return
			}
			request, err := http.NewRequest(
				http.MethodPost, endpoint, bytes.NewReader(payload),
			)
			if err != nil {
				results <- result{err: err}
				return
			}
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-API-Key", apiKey)

			response, err := client.Do(request)
			if err != nil {
				results <- result{err: err}
				return
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			results <- result{status: response.StatusCode}
		}(i)
	}

	workers.Wait()
}

func monitorResults(output io.Writer, results <-chan result, total int) bool {
	started := time.Now()
	lastUpdate := started
	lastCompleted := 0
	completed := 0
	statusCounts := make(map[int]int)
	failures := 0
	var lastError error
	refresh := time.NewTicker(time.Second)
	defer refresh.Stop()

	printProgress := func(now time.Time, final bool) {
		rps := float64(completed-lastCompleted) / now.Sub(lastUpdate).Seconds()
		if final {
			rps = float64(completed) / now.Sub(started).Seconds()
		}
		fmt.Fprintf(output, "\r\x1b[2KRPS: %.1f | Completed: %d/%d | HTTP statuses: %v | Errors: %d",
			rps, completed, total, statusCounts, failures)
		if lastError != nil {
			fmt.Fprintf(output, " | Last error: %q", lastError.Error())
		}
		if final {
			fmt.Fprintln(output)
		}
		lastUpdate = now
		lastCompleted = completed
	}

	for {
		select {
		case r, ok := <-results:
			if !ok {
				printProgress(time.Now(), true)
				return failures == 0 && statusCounts[http.StatusCreated] == total
			}
			completed++
			if r.err != nil {
				failures++
				lastError = r.err
			} else {
				statusCounts[r.status]++
			}
		case <-refresh.C:
			printProgress(time.Now(), false)
		}
	}
}
