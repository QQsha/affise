package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/QQsha/affise/rate" //not a thrid-party package
)

func main() {
	server := &http.Server{
		Addr: ":8080",
	}

	// rate limit middleware
	limiter := rate.NewLimiter(100)
	http.Handle("/parser", limiter.RateLimit(urlParser)) 

	go func() {
		if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP server error: %v", err)
		}
		log.Println("Stopped serving new connections.")
	}()
	
	// graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	shutdownCtx, shutdownRelease := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownRelease()

	if err := server.Shutdown(shutdownCtx); err != nil { 
		log.Fatalf("HTTP shutdown error: %v", err)
	}
	log.Println("Graceful shutdown complete.")
}

type request struct {
	URLs []string `json:"url"`
}

func urlParser(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "error: method now allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	list := request{}
	err := json.NewDecoder(req.Body).Decode(&list)
	if err != nil {
		http.Error(w, fmt.Errorf("error: %w", err).Error(), http.StatusBadRequest)
		return
	}
	// url size limit
	if len(list.URLs) > 20 {
		http.Error(w, "error: too many urls in request", http.StatusBadRequest)
		return
	}

	urlBucket := make([]string, 0, len(list.URLs))
	resultC := make(chan string, len(list.URLs))

	semaphore := make(chan struct{}, 4)

	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	ctxErr := errors.New("request canceled by client")

	go func() {
		defer close(resultC)
		wg := &sync.WaitGroup{}
		for _, u := range list.URLs {
			wg.Add(1)
			go func(url string, wg *sync.WaitGroup, ctx context.Context) {
				defer wg.Done()

				// semaphore for 4 concurrent requests limit
				semaphore <- struct{}{}
				defer func() { <-semaphore }()

				res, err := send(url, ctx)
				// if err != nil, all goroutines stops by context cancel
				if err != nil {
					if ctx.Err() == nil {
						ctxErr = err
						cancel()
					}
					return
				}

				resultC <- res

			}(u, wg, ctx)
		}
		wg.Wait()
	}()

	var count int
	for {
		select {
		case r, ok := <-resultC:
			if !ok {
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(
					struct {
						Count int      `json:"count"`
						Urls  []string `json:"urls"`
					}{
						count,
						urlBucket,
					},
				)
				return
			}
			urlBucket = append(urlBucket, r)
			count++

		// triggers by error, or client cancel
		case <-ctx.Done():
			http.Error(w, fmt.Errorf("error: %w", ctxErr).Error(), http.StatusRequestTimeout)
			return
		}
	}

}

func send(url string, ctx context.Context) (string, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("http.NewRequest(%s) failed: %w", url, err)
	}
	// 1 sec timeout for outer request
	client := &http.Client{Timeout: time.Second}
	req = req.WithContext(ctx)

	res, err := client.Do(req)
	if err != nil {
		return "", err
	}

	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%v responded with status %v", url, res.StatusCode)
	}

	b := new(bytes.Buffer)
	b.ReadFrom(res.Body)
	res.Body.Close()

	return b.String(), nil
}
