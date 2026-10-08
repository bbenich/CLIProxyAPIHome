package respserver

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	appconfig "github.com/router-for-me/CLIProxyAPIHome/internal/config"
	"github.com/router-for-me/CLIProxyAPIHome/internal/home"
)

func TestConfigPublishDoesNotBlockOnStalledSubscriber(t *testing.T) {
	runtimeHome, errRuntime := home.NewRuntime(&appconfig.Config{})
	if errRuntime != nil {
		t.Fatalf("NewRuntime() error = %v", errRuntime)
	}
	ready := make(chan struct{})
	close(ready)

	// The stalled subscriber never reads from its socket.
	stalledServer, stalledClient := net.Pipe()
	defer func() {
		if errClose := stalledClient.Close(); errClose != nil {
			t.Errorf("close stalled client: %v", errClose)
		}
	}()
	stalled := newConfigSubscriptionDelivery(context.Background(), newSafeWriter(stalledServer), ready, make(chan struct{}))
	unsubscribeStalled := runtimeHome.SubscribeConfigYAML(stalled.Write)
	defer unsubscribeStalled()

	healthyOutput := &lockedBuffer{}
	healthy := newConfigSubscriptionDelivery(context.Background(), newSafeWriter(healthyOutput), ready, make(chan struct{}))
	unsubscribeHealthy := runtimeHome.SubscribeConfigYAML(healthy.Write)
	defer unsubscribeHealthy()

	publishDone := make(chan struct{})
	go func() {
		defer close(publishDone)
		for _, marker := range []string{"publish-1", "publish-2", "publish-3"} {
			runtimeHome.PublishConfigYAML([]byte("host: \"\"\nport: 8327\ntest-marker: " + marker + "\n"))
		}
	}()
	waitSubscriptionTestSignal(t, publishDone, "config publish with a stalled subscriber")

	waitSubscriptionTestSignal(t, configSubscriptionDeliveryTail(healthy), "healthy subscriber delivery")
	output := healthyOutput.String()
	if !strings.Contains(output, "publish-3") {
		t.Fatalf("healthy subscriber missing latest config: %q", output)
	}
	assertConfigMarkersOrdered(t, output, "publish-1", "publish-2", "publish-3")

	// Closing the stalled connection must release its sender goroutine.
	if errClose := stalledServer.Close(); errClose != nil {
		t.Fatalf("close stalled server: %v", errClose)
	}
	waitSubscriptionTestSignal(t, configSubscriptionDeliveryTail(stalled), "stalled subscriber teardown")
	if configSubscriptionDeliverySending(stalled) {
		t.Fatal("stalled subscriber sender goroutine still running after connection close")
	}
	if errWrite := stalled.Write([]byte("after-close")); errWrite == nil {
		t.Fatal("Write() after delivery failure error = nil, want failure")
	}
}

func TestConfigSubscriptionDeliveryCoalescesToLatest(t *testing.T) {
	ready := make(chan struct{})
	output := &lockedBuffer{}
	delivery := newConfigSubscriptionDelivery(context.Background(), newSafeWriter(output), ready, make(chan struct{}))

	writesDone := make(chan error, 1)
	go func() {
		for _, marker := range []string{"update-a", "update-b", "update-c"} {
			if errWrite := delivery.Write([]byte(marker)); errWrite != nil {
				writesDone <- errWrite
				return
			}
		}
		writesDone <- nil
	}()
	select {
	case errWrite := <-writesDone:
		if errWrite != nil {
			t.Fatalf("Write() error = %v", errWrite)
		}
	case <-time.After(respPipeDeadline):
		t.Fatal("Write() blocked before the subscription became ready")
	}

	close(ready)
	waitSubscriptionTestSignal(t, configSubscriptionDeliveryTail(delivery), "coalesced delivery")
	got := output.String()
	if strings.Count(got, "message") != 1 || !strings.Contains(got, "update-c") {
		t.Fatalf("coalesced output = %q, want only update-c", got)
	}
	if strings.Contains(got, "update-a") || strings.Contains(got, "update-b") {
		t.Fatalf("coalesced output delivered superseded config: %q", got)
	}
}

func TestConfigSubscriptionDeliveryNeverDeliversOlderAfterNewer(t *testing.T) {
	ready := make(chan struct{})
	close(ready)
	writer := newGatedWriter()
	delivery := newConfigSubscriptionDelivery(context.Background(), newSafeWriter(writer), ready, make(chan struct{}))

	writeConfigDeliveryWithin(t, delivery, "update-a")
	waitSubscriptionTestSignal(t, writer.entered, "in-flight write of update-a")
	writeConfigDeliveryWithin(t, delivery, "update-b")
	writeConfigDeliveryWithin(t, delivery, "update-c")
	close(writer.release)
	waitSubscriptionTestSignal(t, configSubscriptionDeliveryTail(delivery), "queued delivery")

	got := writer.String()
	if strings.Contains(got, "update-b") {
		t.Fatalf("delivery wrote superseded update-b: %q", got)
	}
	assertConfigMarkersOrdered(t, got, "update-a", "update-c")
	if !strings.Contains(got, "update-a") || !strings.Contains(got, "update-c") {
		t.Fatalf("delivery output = %q, want update-a then update-c", got)
	}
}

func TestConfigSubscriptionDeliverySenderExitsAfterUnsubscribe(t *testing.T) {
	runtimeHome, errRuntime := home.NewRuntime(&appconfig.Config{})
	if errRuntime != nil {
		t.Fatalf("NewRuntime() error = %v", errRuntime)
	}
	ready := make(chan struct{})
	close(ready)
	output := &lockedBuffer{}
	delivery := newConfigSubscriptionDelivery(context.Background(), newSafeWriter(output), ready, make(chan struct{}))
	unsubscribe := runtimeHome.SubscribeConfigYAML(delivery.Write)

	runtimeHome.PublishConfigYAML([]byte("host: \"\"\nport: 8327\ntest-marker: before-unsubscribe\n"))
	waitSubscriptionTestSignal(t, configSubscriptionDeliveryTail(delivery), "delivery before unsubscribe")
	unsubscribe()
	if configSubscriptionDeliverySending(delivery) {
		t.Fatal("sender goroutine still running after delivery became idle")
	}

	runtimeHome.PublishConfigYAML([]byte("host: \"\"\nport: 8327\ntest-marker: after-unsubscribe\n"))
	if configSubscriptionDeliverySending(delivery) {
		t.Fatal("publish after unsubscribe started a sender goroutine")
	}
	if strings.Contains(output.String(), "after-unsubscribe") {
		t.Fatalf("unsubscribed delivery received config: %q", output.String())
	}
}

func TestConfigSubscriptionDeliverySenderExitsWhenConnectionEndsBeforeReady(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	output := &lockedBuffer{}
	delivery := newConfigSubscriptionDelivery(ctx, newSafeWriter(output), make(chan struct{}), make(chan struct{}))

	writeConfigDeliveryWithin(t, delivery, "pending-update")
	cancel()
	waitSubscriptionTestSignal(t, configSubscriptionDeliveryTail(delivery), "sender exit after connection end")
	if configSubscriptionDeliverySending(delivery) {
		t.Fatal("sender goroutine still running after connection end")
	}
	if output.Len() != 0 {
		t.Fatalf("canceled delivery wrote %q", output.String())
	}
	if errWrite := delivery.Write([]byte("late-update")); !errors.Is(errWrite, context.Canceled) {
		t.Fatalf("Write() after cancel error = %v, want context.Canceled", errWrite)
	}
}

func TestConfigSubscriptionDeliveryErrorRemovesSubscriber(t *testing.T) {
	runtimeHome, errRuntime := home.NewRuntime(&appconfig.Config{})
	if errRuntime != nil {
		t.Fatalf("NewRuntime() error = %v", errRuntime)
	}
	ready := make(chan struct{})
	close(ready)
	delivery := newConfigSubscriptionDelivery(context.Background(), newSafeWriter(failingSubscriptionWriter{}), ready, make(chan struct{}))
	var calls int
	unsubscribe := runtimeHome.SubscribeConfigYAML(func(payload []byte) error {
		calls++
		return delivery.Write(payload)
	})
	defer unsubscribe()

	payload := []byte("host: \"\"\nport: 8327\n")
	runtimeHome.PublishConfigYAML(payload)
	waitSubscriptionTestSignal(t, configSubscriptionDeliveryTail(delivery), "failed delivery")
	// The recorded delivery failure is reported on the next publish, which removes the subscriber.
	runtimeHome.PublishConfigYAML(payload)
	runtimeHome.PublishConfigYAML(payload)
	if calls != 2 {
		t.Fatalf("subscriber calls = %d, want 2 (removed after reported failure)", calls)
	}
}

// writeConfigDeliveryWithin fails the test when Write blocks on the connection.
func writeConfigDeliveryWithin(t *testing.T, delivery *configSubscriptionDelivery, payload string) {
	t.Helper()
	result := make(chan error, 1)
	go func() { result <- delivery.Write([]byte(payload)) }()
	select {
	case errWrite := <-result:
		if errWrite != nil {
			t.Fatalf("Write(%s) error = %v", payload, errWrite)
		}
	case <-time.After(respPipeDeadline):
		t.Fatalf("Write(%s) blocked on the subscriber connection", payload)
	}
}

func assertConfigMarkersOrdered(t *testing.T, output string, markers ...string) {
	t.Helper()
	last := -1
	for _, marker := range markers {
		index := strings.Index(output, marker)
		if index < 0 {
			continue
		}
		if index < last {
			t.Fatalf("config %s delivered after a newer config: %q", marker, output)
		}
		last = index
	}
}

func configSubscriptionDeliverySending(delivery *configSubscriptionDelivery) bool {
	delivery.queueMu.Lock()
	defer delivery.queueMu.Unlock()
	return delivery.sending
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(payload []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(payload)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *lockedBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

// gatedWriter blocks its first write until release is closed.
type gatedWriter struct {
	lockedBuffer
	entered   chan struct{}
	release   chan struct{}
	enterOnce sync.Once
}

func newGatedWriter() *gatedWriter {
	return &gatedWriter{entered: make(chan struct{}), release: make(chan struct{})}
}

func (w *gatedWriter) Write(payload []byte) (int, error) {
	w.enterOnce.Do(func() {
		close(w.entered)
	})
	<-w.release
	return w.lockedBuffer.Write(payload)
}
