package sync

import (
	"context"
	stdsync "sync"
)

// transferWorkers bounds concurrent store requests (pack uploads and
// downloads, manifest fetches). Enough to hide per-request latency on Drive
// and S3 without tripping their rate limits.
const transferWorkers = 6

// parallel runs fn(i) for i in [0,n) on at most workers goroutines. On the
// first error it stops starting new work, cancels ctx for the running calls
// and returns that error.
func parallel(ctx context.Context, n, workers int, fn func(ctx context.Context, i int) error) error {
	if n == 0 {
		return ctx.Err()
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg    stdsync.WaitGroup
		once  stdsync.Once
		first error
		sem   = make(chan struct{}, max(workers, 1))
	)
loop:
	for i := 0; i < n; i++ {
		select {
		case sem <- struct{}{}:
		case <-cctx.Done():
			break loop
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := fn(cctx, i); err != nil {
				once.Do(func() { first = err; cancel() })
			}
		}(i)
	}
	wg.Wait()
	if first != nil {
		return first
	}
	return ctx.Err()
}
