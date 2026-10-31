package dmlive

import (
	"context"
	"net"
	"sync"
	"time"
)

// streamTee bridges the relay's single-consumer f2m socket to multiple
// consumers: the relay's `-listen 1` output can feed exactly one client, but
// play&record needs two (mpv plays, a recorder ffmpeg copies). The tee dials
// f2m itself and fans the identical byte stream out to every subscriber.
// Recording through this path instead of mpv's stream-record also fixes the
// live-recording packet loss (mpv's live demuxer withholds most packets from
// its recorder; ffmpeg -c copy over the tee loses nothing).
type streamTee struct {
	upstream string

	mu    sync.Mutex
	conns map[*teeSink]struct{}
}

// teeSink is one subscriber with its own bounded queue, so a slow consumer
// only loses itself, never the others.
type teeSink struct {
	conn net.Conn
	ch   chan []byte
}

const teeSinkQueue = 1024 // chunks of 32KB ≈ 32MB slack before being cut

func newStreamTee(upstream string) *streamTee {
	return &streamTee{upstream: upstream, conns: map[*teeSink]struct{}{}}
}

// Run dials the upstream in a reconnect loop until ctx ends and broadcasts
// everything it reads to the current subscribers.
func (t *streamTee) Run(ctx context.Context) {
	for ctx.Err() == nil {
		conn, err := net.Dial("tcp", hostOf(t.upstream))
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
			continue
		}
		t.pump(ctx, conn)
		select {
		case <-ctx.Done():
			t.hardClose()
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// pump reads one upstream connection until it drops, fanning out to
// subscribers, then closes them all (a fresh matroska stream begins on the
// next dial — mpv idles until the next loadfile, recorders finalize on EOF).
func (t *streamTee) pump(ctx context.Context, upstream net.Conn) {
	defer upstream.Close()
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = upstream.Close()
		case <-done:
		}
	}()
	defer close(done)

	buf := make([]byte, 32*1024)
	for {
		n, err := upstream.Read(buf)
		if n > 0 {
			t.broadcast(copyBytes(buf[:n]))
		}
		if err != nil {
			// Upstream ended: let each subscriber's queue drain fully (the
			// recorder's ffmpeg needs the tail bytes to finalize cleanly),
			// then their writers close the connections themselves.
			t.finishAll()
			return
		}
	}
}

// broadcast queues one chunk for every subscriber; a subscriber whose queue
// is full is cut off rather than stalling the read loop.
func (t *streamTee) broadcast(chunk []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for s := range t.conns {
		select {
		case s.ch <- chunk:
		default:
			delete(t.conns, s)
			close(s.ch)
			_ = s.conn.Close()
		}
	}
}

// finishAll closes every subscriber's queue on upstream EOF; the drain
// goroutines flush what remains and then close their connections.
func (t *streamTee) finishAll() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for s := range t.conns {
		close(s.ch)
		delete(t.conns, s)
	}
}

// hardClose cuts everything immediately (session shutdown).
func (t *streamTee) hardClose() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for s := range t.conns {
		_ = s.conn.Close()
		delete(t.conns, s)
	}
}

// Serve accepts subscribers on ln until ctx ends, each with a writer
// goroutine draining its queue.
func (t *streamTee) Serve(ctx context.Context, ln net.Listener) {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		s := &teeSink{conn: c, ch: make(chan []byte, teeSinkQueue)}
		t.mu.Lock()
		t.conns[s] = struct{}{}
		t.mu.Unlock()
		go t.drain(s)
	}
}

func (t *streamTee) drain(s *teeSink) {
	defer func() {
		t.mu.Lock()
		delete(t.conns, s)
		t.mu.Unlock()
		_ = s.conn.Close()
	}()
	for chunk := range s.ch {
		_ = s.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := s.conn.Write(chunk); err != nil {
			return
		}
	}
}

func hostOf(tcpURL string) string {
	// "tcp://127.0.0.1:1234" -> "127.0.0.1:1234"
	if len(tcpURL) > 6 && tcpURL[:6] == "tcp://" {
		return tcpURL[6:]
	}
	return tcpURL
}

func copyBytes(b []byte) []byte {
	out := make([]byte, len(b))
	copy(out, b)
	return out
}
