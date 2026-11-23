package websocket

import (
	"sync"
	"testing"
)

// newTestClient builds a client with no WebSocket connection. Every test here
// exercises the send/close lifecycle, which never touches the connection.
func newTestClient() *Client {
	return &Client{send: make(chan []byte, 4)}
}

func TestClientTrySendDeliversWhileOpen(t *testing.T) {
	c := newTestClient()

	if !c.trySend([]byte("first")) {
		t.Fatal("trySend on an open client should succeed")
	}

	select {
	case got := <-c.send:
		if string(got) != "first" {
			t.Errorf("got %q, want %q", got, "first")
		}
	default:
		t.Fatal("message was not queued")
	}
}

func TestClientTrySendReportsFullBuffer(t *testing.T) {
	c := &Client{send: make(chan []byte, 1)}

	if !c.trySend([]byte("a")) {
		t.Fatal("first send should fit")
	}
	// A browser that stops reading must not block metric streaming for
	// everyone else, so the message is dropped rather than queued.
	if c.trySend([]byte("b")) {
		t.Error("trySend should report false once the buffer is full")
	}
}

// TestClientTrySendAfterCloseDoesNotPanic is the regression this file exists
// for. The metric streamer sends from goroutines that outlive the hub lock, so
// it can reach a client that has just disconnected. Sending on the closed
// channel directly panics, and a panic in that goroutine terminates the whole
// web UI process.
func TestClientTrySendAfterCloseDoesNotPanic(t *testing.T) {
	c := newTestClient()
	c.close()

	if c.trySend([]byte("late")) {
		t.Error("trySend should refuse to deliver to a closed client")
	}
}

func TestClientCloseIsIdempotent(t *testing.T) {
	c := newTestClient()

	c.close()
	// Both ReadPump's unregister and a shutdown path can reach this; closing
	// twice must not panic.
	c.close()

	if _, open := <-c.send; open {
		t.Error("send channel should be closed")
	}
}

// TestClientConcurrentSendAndClose reproduces the original crash. Run with
// -race: without the lock this reliably panics with "send on closed channel".
func TestClientConcurrentSendAndClose(t *testing.T) {
	const rounds = 200

	for i := 0; i < rounds; i++ {
		c := &Client{send: make(chan []byte, 1)}

		// Drain so senders are not merely bouncing off a full buffer.
		done := make(chan struct{})
		go func() {
			defer close(done)
			for range c.send {
			}
		}()

		var wg sync.WaitGroup
		for s := 0; s < 4; s++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for n := 0; n < 25; n++ {
					c.trySend([]byte("metric"))
				}
			}()
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			c.close()
		}()

		wg.Wait()
		<-done
	}
}

// TestClientCloseUnblocksWritePumpSignal verifies the close still signals the
// writer to stop, which is what the closed channel is for.
func TestClientCloseUnblocksWritePumpSignal(t *testing.T) {
	c := newTestClient()
	c.close()

	// A receive on a closed channel returns immediately with ok == false,
	// which is how WritePump learns to shut down.
	if _, ok := <-c.send; ok {
		t.Error("expected the closed channel to report !ok")
	}
}
