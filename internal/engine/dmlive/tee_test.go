package dmlive

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

// The tee must deliver the identical upstream byte stream to every
// subscriber, and cut them all off (EOF) when the upstream drops.
func TestStreamTeeFansOutIdenticalBytes(t *testing.T) {
	upLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback unavailable: %v", err)
	}
	defer upLn.Close()

	payload := make([]byte, 300*1024) // spans many 32KB read chunks
	for i := range payload {
		payload[i] = byte(i)
	}

	teeLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback unavailable: %v", err)
	}
	defer teeLn.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tee := newStreamTee("tcp://" + upLn.Addr().String())
	go tee.Run(ctx)
	go tee.Serve(ctx, teeLn)

	// Wait for the tee to connect upstream, THEN attach subscribers, so the
	// fan-out set is complete before data flows.
	up, err := upLn.Accept()
	if err != nil {
		t.Fatalf("tee did not connect upstream: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	dial := func() net.Conn {
		c, err := net.Dial("tcp", teeLn.Addr().String())
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		return c
	}
	a, b := dial(), dial()
	time.Sleep(300 * time.Millisecond) // let Serve register both

	got := make([][]byte, 2)
	done := make(chan struct{}, 2)
	reader := func(c net.Conn, i int) {
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		out, _ := io.ReadAll(c)
		got[i] = out
		done <- struct{}{}
	}
	go reader(a, 0)
	go reader(b, 1)

	if _, err := up.Write(payload); err != nil {
		t.Fatalf("write upstream: %v", err)
	}
	_ = up.Close() // upstream drop -> tee cuts subscribers -> EOF for both

	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(6 * time.Second):
			t.Fatalf("subscriber %d never saw EOF", i)
		}
	}
	if string(got[0]) != string(payload) {
		t.Fatalf("subscriber 0 got %d bytes, want %d", len(got[0]), len(payload))
	}
	if string(got[1]) != string(got[0]) {
		t.Fatalf("subscribers diverge: %d vs %d bytes", len(got[1]), len(got[0]))
	}
}
