package respserver

import (
	"context"
	"net"
	"sync"
)

// configSubscriptionDelivery delivers config updates to one subscriber without
// blocking the publisher. Write stores the payload in a one-slot mailbox where a
// newer config replaces an undelivered older one, and a per-subscription sender
// goroutine writes it to the connection. The sender only runs while there is
// work, so it exits once the mailbox is drained, the connection context ends, or
// a delivery fails.
type configSubscriptionDelivery struct {
	ctx     context.Context
	writer  *safeWriter
	ready   <-chan struct{}
	aborted <-chan struct{}

	// queueMu guards the mailbox state below.
	queueMu sync.Mutex
	// tail is closed once the most recently written payload, or a newer one,
	// has been delivered or dropped.
	tail <-chan struct{}
	// pending is the newest undelivered payload, or nil.
	pending []byte
	// pendingWaiters are closed when pending (or a newer payload) is handled.
	pendingWaiters []chan struct{}
	// sending reports whether the sender goroutine is running.
	sending bool
	// err is the first delivery failure; once set, every Write returns it.
	err error
}

func newConfigSubscriptionDelivery(
	ctx context.Context,
	writer *safeWriter,
	ready <-chan struct{},
	aborted <-chan struct{},
) *configSubscriptionDelivery {
	if ctx == nil {
		ctx = context.Background()
	}
	queueHead := make(chan struct{})
	close(queueHead)
	return &configSubscriptionDelivery{
		ctx:     ctx,
		writer:  writer,
		ready:   ready,
		aborted: aborted,
		tail:    queueHead,
	}
}

// Write queues payload for asynchronous delivery and returns without waiting for
// the connection. It returns the recorded failure once a delivery has failed so
// the publisher drops the subscription.
func (d *configSubscriptionDelivery) Write(payload []byte) error {
	if d == nil || d.writer == nil {
		return net.ErrClosed
	}
	if errContext := d.ctx.Err(); errContext != nil {
		return errContext
	}
	if payload == nil {
		payload = []byte{}
	}

	d.queueMu.Lock()
	defer d.queueMu.Unlock()
	if d.err != nil {
		return d.err
	}
	done := make(chan struct{})
	d.tail = done
	d.pending = payload
	d.pendingWaiters = append(d.pendingWaiters, done)
	if !d.sending {
		d.sending = true
		go d.run()
	}
	return nil
}

// run writes queued payloads in order until the mailbox is empty or delivery fails.
// It waits for the initial snapshot before taking a payload, so updates published
// meanwhile coalesce to the newest one.
func (d *configSubscriptionDelivery) run() {
	errDeliver := d.waitReady()
	d.queueMu.Lock()
	for errDeliver == nil && d.pending != nil {
		payload, waiters := d.pending, d.pendingWaiters
		d.pending, d.pendingWaiters = nil, nil
		d.queueMu.Unlock()

		errDeliver = d.deliver(payload)

		d.queueMu.Lock()
		closeConfigDeliveryWaiters(waiters)
	}
	if errDeliver != nil {
		d.err = errDeliver
		closeConfigDeliveryWaiters(d.pendingWaiters)
		d.pending, d.pendingWaiters = nil, nil
	}
	d.sending = false
	d.queueMu.Unlock()
}

// waitReady waits until the initial snapshot has been sent.
func (d *configSubscriptionDelivery) waitReady() error {
	select {
	case <-d.ready:
	case <-d.ctx.Done():
		return d.ctx.Err()
	}
	if errContext := d.ctx.Err(); errContext != nil {
		return errContext
	}
	select {
	case <-d.aborted:
		return net.ErrClosed
	default:
	}
	return nil
}

// deliver writes one payload to the subscriber connection.
func (d *configSubscriptionDelivery) deliver(payload []byte) error {
	if errContext := d.ctx.Err(); errContext != nil {
		return errContext
	}
	return d.writer.WriteDispatchReply(subscriptionMessage(configSubscriptionChannel, payload))
}

func closeConfigDeliveryWaiters(waiters []chan struct{}) {
	for _, waiter := range waiters {
		close(waiter)
	}
}
