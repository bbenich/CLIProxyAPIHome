package home

import (
	"context"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

type usagePayloadItem struct {
	payload    string
	receivedAt time.Time
}

// clusterUsageDrainTimeout bounds how long shutdown waits for queued usage
// payloads to be persisted.
var clusterUsageDrainTimeout = 5 * time.Second

type usagePayloadQueue struct {
	mu       sync.Mutex
	cond     *sync.Cond
	items    []usagePayloadItem
	head     int
	closed   bool
	inFlight bool
}

func newUsagePayloadQueue() *usagePayloadQueue {
	q := &usagePayloadQueue{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *usagePayloadQueue) Push(payload string, receivedAt time.Time) bool {
	if q == nil {
		return false
	}
	if receivedAt.IsZero() {
		receivedAt = time.Now().UTC()
	} else {
		receivedAt = receivedAt.UTC()
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return false
	}
	q.items = append(q.items, usagePayloadItem{payload: payload, receivedAt: receivedAt})
	q.cond.Signal()
	return true
}

func (q *usagePayloadQueue) Pop() (usagePayloadItem, bool) {
	if q == nil {
		return usagePayloadItem{}, false
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	q.inFlight = false
	for q.head >= len(q.items) && !q.closed {
		q.cond.Wait()
	}
	// A closed queue still hands out its backlog so shutdown can drain it.
	if q.head >= len(q.items) {
		return usagePayloadItem{}, false
	}

	q.inFlight = true
	item := q.items[q.head]
	q.items[q.head] = usagePayloadItem{}
	q.head++
	q.compactLocked()
	return item, true
}

// Close stops accepting new payloads. Already queued payloads remain poppable.
func (q *usagePayloadQueue) Close() {
	if q == nil {
		return
	}

	q.mu.Lock()
	q.closed = true
	q.cond.Broadcast()
	q.mu.Unlock()
}

// Discard closes the queue, drops its backlog and returns how many payloads
// were not persisted, including one still being written.
func (q *usagePayloadQueue) Discard() int {
	if q == nil {
		return 0
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	dropped := len(q.items) - q.head
	if q.inFlight {
		dropped++
	}
	q.closed = true
	q.items = nil
	q.head = 0
	q.inFlight = false
	q.cond.Broadcast()
	return dropped
}

func (q *usagePayloadQueue) compactLocked() {
	if q.head < 1024 || q.head*2 < len(q.items) {
		return
	}
	next := append([]usagePayloadItem(nil), q.items[q.head:]...)
	q.items = next
	q.head = 0
}

func (r *Runtime) startClusterUsageWriter(ctx context.Context) {
	if r == nil || r.clusterAdapter == nil || !r.clusterAdapter.Enabled() {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	store, ok := r.clusterAdapter.(clusterUsageStore)
	if !ok || store == nil {
		log.Errorf("cluster usage store is unavailable")
		return
	}

	queue := newUsagePayloadQueue()
	r.clusterUsageQueueMu.Lock()
	if r.clusterUsageQueue != nil {
		r.clusterUsageQueueMu.Unlock()
		return
	}
	// Writes outlive ctx so shutdown can drain the backlog; stopClusterUsageWriter
	// bounds the drain and cancels writeCtx afterwards.
	writeCtx, cancelWrite := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	r.clusterUsageQueue = queue
	r.clusterUsageWriterDone = done
	r.clusterUsageWriterCancel = cancelWrite
	r.clusterUsageQueueMu.Unlock()

	go func() {
		select {
		case <-ctx.Done():
			queue.Close()
		case <-done:
		}
	}()
	go func() {
		defer close(done)
		r.runClusterUsageWriter(writeCtx, store, queue)
	}()
}

func (r *Runtime) stopClusterUsageWriter() {
	if r == nil {
		return
	}

	r.clusterUsageQueueMu.Lock()
	queue := r.clusterUsageQueue
	done := r.clusterUsageWriterDone
	cancelWrite := r.clusterUsageWriterCancel
	r.clusterUsageQueue = nil
	r.clusterUsageWriterDone = nil
	r.clusterUsageWriterCancel = nil
	r.clusterUsageQueueMu.Unlock()
	if queue == nil {
		return
	}
	queue.Close()
	if done != nil {
		timer := time.NewTimer(clusterUsageDrainTimeout)
		select {
		case <-done:
		case <-timer.C:
			if dropped := queue.Discard(); dropped > 0 {
				log.WithField("dropped", dropped).Warn("cluster usage drain deadline exceeded; dropping unpersisted usage payloads")
			}
		}
		timer.Stop()
	}
	if cancelWrite != nil {
		cancelWrite()
	}
}

func (r *Runtime) getClusterUsageQueue() *usagePayloadQueue {
	if r == nil {
		return nil
	}

	r.clusterUsageQueueMu.Lock()
	defer r.clusterUsageQueueMu.Unlock()
	return r.clusterUsageQueue
}

func (r *Runtime) runClusterUsageWriter(ctx context.Context, store clusterUsageStore, queue *usagePayloadQueue) {
	if store == nil || queue == nil {
		return
	}

	for {
		item, ok := queue.Pop()
		if !ok {
			return
		}
		if strings.TrimSpace(item.payload) == "" {
			continue
		}
		if errStoreUsagePayload := store.StoreUsagePayload(ctx, item.payload, item.receivedAt); errStoreUsagePayload != nil {
			log.Errorf("usage database async write error: %v", errStoreUsagePayload)
		}
	}
}
