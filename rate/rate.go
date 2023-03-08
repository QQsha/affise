package rate

import (
	"errors"
	"net/http"
	"sync"
)

var ErrRateLimitReached = errors.New("rate limit reached")

type limiter struct {
	limit int
	count int
	mux   *sync.Mutex
}

func NewLimiter(limit int) limiter {
	return limiter{
		limit: limit,
		mux:   &sync.Mutex{},
	}
}

func (l *limiter) incr() error {
	l.mux.Lock()
	defer l.mux.Unlock()

	if l.count > l.limit {
		return ErrRateLimitReached
	}

	l.count++
	return nil
}

func (l *limiter) decr() {
	l.mux.Lock()

	l.count--
	l.mux.Unlock()

}

func (l *limiter) RateLimit(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := l.incr(); err == ErrRateLimitReached {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		defer l.decr()
		handler.ServeHTTP(w, r)
	}
}
