package streamer

import (
	"context"
	"io"
	"net"
	"net/http"
	"time"
)

// GenUA is the user agent dmlive presents to CDNs.
const GenUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:146.0) Gecko/20100101 Firefox/146.0"

// streamHTTPClient has a connect timeout but no overall deadline: the body
// streams for the whole session.
func streamHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
		},
	}
}

// AcceptConsumer blocks until ffmpeg dials the streamer's listener.
func AcceptConsumer(ctx context.Context, ln net.Listener) (net.Conn, error) {
	type result struct {
		c   net.Conn
		err error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := ln.Accept()
		ch <- result{c, err}
	}()
	select {
	case r := <-ch:
		return r.c, r.err
	case <-ctx.Done():
		go func() {
			r := <-ch
			if r.c != nil {
				_ = r.c.Close()
			}
		}()
		return nil, ctx.Err()
	}
}

// RunFLV downloads an FLV live stream and relays it to the consumer
// connected on videoLn. A watchdog aborts after ~10s without data, ending
// the session so the outer loop can reconnect. onReady fires once the
// source starts producing bytes (Rust's StreamReady).
func RunFLV(ctx context.Context, url, referer, cookie string, pliveBili bool, videoLn net.Listener, onReady func()) error {
	conn, err := AcceptConsumer(ctx, videoLn)
	if err != nil {
		return err
	}
	defer conn.Close()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", GenUA)
	req.Header.Set("Referer", referer)
	if pliveBili && cookie != "" {
		req.Header.Set("Cookie", cookie)
	}

	resp, err := streamHTTPClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if onReady != nil {
		onReady()
	}

	watchdog := 0
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	watchdogDone := make(chan struct{})
	go func() {
		defer close(watchdogDone)
		for range ticker.C {
			watchdog++
			if watchdog > 10 { // "connection too slow"
				return
			}
		}
	}()

	buf := make([]byte, 64*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := conn.Write(buf[:n]); werr != nil {
				return werr
			}
			watchdog = 0
		}
		if rerr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if rerr == io.EOF {
				return nil // flv downloader exit normally
			}
			return rerr
		}
		select {
		case <-watchdogDone:
			return nil
		default:
		}
	}
}
