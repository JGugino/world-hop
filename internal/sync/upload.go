package sync

import (
	"context"
	stdsync "sync"

	"github.com/JGugino/world-hopper/internal/store"
)

// packUploader uploads packs on a bounded number of goroutines while the
// caller keeps reading and chunking the next files.
type packUploader struct {
	ctx    context.Context
	cancel context.CancelFunc
	st     store.Store
	sem    chan struct{}
	wg     stdsync.WaitGroup
	mu     stdsync.Mutex
	err    error
}

func newPackUploader(ctx context.Context, st store.Store, workers int) *packUploader {
	cctx, cancel := context.WithCancel(ctx)
	return &packUploader{ctx: cctx, cancel: cancel, st: st, sem: make(chan struct{}, max(workers, 1))}
}

func (u *packUploader) setErr(err error) {
	u.mu.Lock()
	if u.err == nil {
		u.err = err
		u.cancel()
	}
	u.mu.Unlock()
}

func (u *packUploader) failure() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.err
}

// put starts uploading data under name, waiting while the limit of uploads in
// flight is reached. It returns the first failure of any earlier upload.
func (u *packUploader) put(name string, data []byte) error {
	select {
	case u.sem <- struct{}{}:
	case <-u.ctx.Done():
		if err := u.failure(); err != nil {
			return err
		}
		return u.ctx.Err()
	}
	u.wg.Add(1)
	go func() {
		defer u.wg.Done()
		defer func() { <-u.sem }()
		if err := u.st.PutPack(u.ctx, name, data); err != nil {
			u.setErr(err)
		}
	}()
	return u.failure()
}

// wait blocks until every started upload finished and returns the first error.
func (u *packUploader) wait() error {
	u.wg.Wait()
	defer u.cancel()
	if err := u.failure(); err != nil {
		return err
	}
	return u.ctx.Err()
}
