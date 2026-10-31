// Command dmlive is the pure-Go rewrite of Rust dmlive: play or record a
// live stream (or dump video danmaku) with danmaku muxed as ASS subtitle
// tracks. Flags match the Rust CLI.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"revda/internal/engine/dmlive"
)

func main() {
	var (
		url         string
		record      bool
		downloadDM  bool
		wait        uint64
		logLevel    int
		httpAddress string
		quiet       bool
		tcp         bool
		plive       bool
	)
	flag.StringVar(&url, "u", "", "set the http url")
	flag.StringVar(&url, "url", "", "set the http url")
	flag.BoolVar(&record, "r", false, "record")
	flag.BoolVar(&record, "record", false, "record")
	flag.BoolVar(&downloadDM, "download-dm", false, "download danmaku only")
	flag.Uint64Var(&wait, "w", 0, "wait interval seconds between resolve rounds")
	flag.Uint64Var(&wait, "wait-interval", 0, "wait interval seconds between resolve rounds")
	flag.IntVar(&logLevel, "log-level", 3, "log level: 1 debug, 2 info, 3 warn, 4 error")
	flag.StringVar(&httpAddress, "http-address", "", "serve the recording as http matroska")
	flag.BoolVar(&quiet, "q", false, "do not print danmaku")
	flag.BoolVar(&quiet, "quiet", false, "do not print danmaku")
	flag.BoolVar(&tcp, "tcp", false, "accepted for CLI compatibility (no effect)")
	flag.BoolVar(&plive, "plive", false, "send cookies for persistent bilibili live recording")
	flag.Parse()

	if url == "" {
		fmt.Fprintln(os.Stderr, "dmlive: -u/--url is required")
		flag.Usage()
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := dmlive.Run(ctx, dmlive.Options{
		URL:          url,
		Record:       record,
		DownloadDM:   downloadDM,
		WaitInterval: wait,
		Quiet:        quiet,
		Plive:        plive,
		HTTPAddress:  httpAddress,
		LogLevel:     logLevel,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "dmlive:", err)
		os.Exit(1)
	}
}
