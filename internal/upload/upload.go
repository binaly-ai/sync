// Package upload sends file bytes to the upload URL the server handed out, with retries and an
// optional bandwidth limit shared by every upload.
package upload

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
)

// ErrExpired means the upload URL is no longer valid; the file is re-reported next run and
// gets a fresh one.
var ErrExpired = errors.New("upload URL expired")

// Limiter is a token bucket in bytes per second, shared across uploads. Zero means unlimited.
type Limiter struct {
	mu     sync.Mutex
	rate   int64
	tokens int64
	last   time.Time
}

// NewLimiter limits to kbps kilobytes per second (0 = unlimited).
func NewLimiter(kbps int) *Limiter {
	return &Limiter{rate: int64(kbps) * 1024, last: time.Now()}
}

func (l *Limiter) wait(ctx context.Context, n int) error {
	if l == nil || l.rate <= 0 {
		return nil
	}
	for {
		l.mu.Lock()
		now := time.Now()
		l.tokens += int64(now.Sub(l.last).Seconds() * float64(l.rate))
		if l.tokens > l.rate {
			l.tokens = l.rate
		}
		l.last = now
		if l.tokens >= int64(n) {
			l.tokens -= int64(n)
			l.mu.Unlock()
			return nil
		}
		need := time.Duration(float64(int64(n)-l.tokens) / float64(l.rate) * float64(time.Second))
		l.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(need):
		}
	}
}

type limitedReader struct {
	ctx context.Context
	r   io.Reader
	l   *Limiter
}

func (lr *limitedReader) Read(p []byte) (int, error) {
	if len(p) > 64<<10 {
		p = p[:64<<10]
	}
	n, err := lr.r.Read(p)
	if n > 0 {
		if werr := lr.l.wait(lr.ctx, n); werr != nil {
			return n, werr
		}
	}
	return n, err
}

var client = &http.Client{Timeout: 0} // a large file on a slow uplink takes as long as it takes

// Put sends a file's bytes to url, retrying network failures and server errors.
func Put(ctx context.Context, url, path string, size int64, lim *Limiter) error {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * 5 * time.Second):
			}
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, "PUT", url, &limitedReader{ctx: ctx, r: f, l: lim})
		if err != nil {
			f.Close()
			return err
		}
		req.ContentLength = size
		req.Header.Set("content-type", "application/octet-stream")
		res, err := client.Do(req)
		f.Close()
		if err != nil {
			last = err
			continue
		}
		io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		switch {
		case res.StatusCode == 200:
			return nil
		case res.StatusCode == 409:
			// Already there: an earlier attempt landed but its answer was lost. The commit
			// that follows checks the bytes against the hash either way.
			return nil
		case res.StatusCode == 403:
			return ErrExpired
		case res.StatusCode >= 500:
			last = fmt.Errorf("upload: HTTP %d", res.StatusCode)
		default:
			return fmt.Errorf("upload refused: HTTP %d", res.StatusCode)
		}
	}
	return last
}
