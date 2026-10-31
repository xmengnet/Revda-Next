// Package ffmpegctl drives the ffmpeg relay: it muxes the downloaded live
// stream (or direct DASH URLs) together with the danmaku subtitle stream
// into a Matroska flow that mpv plays or a recorder stores, mirroring Rust
// dmlive's FfmpegControl.
package ffmpegctl

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
)

type RunMode int

const (
	ModePlay RunMode = iota
	ModeRecord
)

type StreamType int

const (
	TFLV  StreamType = iota
	THLS0            // ts segments (douyu & co): needs the pre-ffmpeg flv conversion
	THLS1            // m4s segments (bilibili)
	TDASH
)

// DashInput is one direct URL input with its own UA/Referer.
type DashInput struct {
	URL     string
	UA      string
	Referer string
}

type Options struct {
	Mode       RunMode
	StreamType StreamType
	Title      string

	DashInputs []DashInput

	VideoURL   string // tcp://... source for FLV/HLS1, pre-ff source for HLS0
	DanmakuURL string // tcp://... danmaku subtitle stream
	Output     string // listen target: tcp://f2m or the --http-address value
}

// FF is one running ffmpeg relay instance.
type FF struct {
	cmd    *exec.Cmd
	cmdCtx context.Context
	stdin  io.WriteCloser

	// OnVideoInfo fires once with the stream resolution parsed from ffmpeg's
	// stderr; OnOutputReady fires once the danmaku subtitle track shows up
	// in the output probe (Rust's DMLMessage::FfmpegOutputReady).
	OnVideoInfo   func(w, h int)
	OnOutputReady func()
}

var (
	resRe = regexp.MustCompile(`Stream #[0-9].+? Video:.*?\D(\d{3,5})x(\d{2,5})\D.*`)
	dmRe  = regexp.MustCompile(`Stream #[0-9:]+\s*Subtitle:\s*ass`)
)

// Run spawns the ffmpeg relay (and the pre-ffmpeg converter for ts HLS).
// The main ffmpeg is killed when ctx ends; Quit() ends it gracefully.
func Run(ctx context.Context, opts Options) (*FF, error) {
	f := &FF{cmdCtx: ctx}

	args := []string{"-y", "-xerror", "-hide_banner", "-nostats"}
	switch opts.StreamType {
	case TDASH:
		for _, in := range opts.DashInputs {
			if in.UA != "" {
				args = append(args, "-user_agent", in.UA)
			}
			if in.Referer != "" {
				args = append(args, "-headers", "Referer: "+in.Referer)
			}
			args = append(args, "-i", in.URL)
		}
		args = append(args, "-i", opts.DanmakuURL)
		if len(opts.DashInputs) >= 2 {
			args = append(args, "-map", "0:v:0?", "-map", "1:a:0?", "-map", "2:s:0", "-map", "2:s:1?")
		} else {
			// single direct input: video+audio in stream 0, danmaku is input 1
			args = append(args, "-map", "0:v:0?", "-map", "0:a:0?", "-map", "1:s:0", "-map", "1:s:1?")
		}
	case THLS0:
		args = append(args, "-i", "-")
		args = append(args, "-i", opts.DanmakuURL)
		args = append(args, "-map", "0:v:0?", "-map", "0:a:0?", "-map", "1:s:0", "-map", "1:s:1?")
	default: // FLV and HLS1 read the socket directly
		args = append(args, "-i", opts.VideoURL)
		args = append(args, "-i", opts.DanmakuURL)
		args = append(args, "-map", "0:v:0?", "-map", "0:a:0?", "-map", "1:s:0", "-map", "1:s:1?")
	}
	args = append(args,
		"-c:v", "copy", "-c:a", "copy", "-c:s", "copy",
		"-metadata", "title="+opts.Title,
		"-f", "matroska",
		"-listen", "1", opts.Output,
	)

	cmd := exec.Command("ffmpeg", args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ffmpegctl: spawn ffmpeg: %w", err)
	}
	f.cmd = cmd
	f.stdin = stdin

	if opts.StreamType == THLS0 {
		if err := f.runPreFF(ctx, opts.VideoURL); err != nil {
			_ = cmd.Process.Kill()
			return nil, err
		}
	}

	go f.parseStderr(stderr)
	go func() {
		<-ctx.Done()
		_ = f.stdin.Close()
	}()

	return f, nil
}

// runPreFF converts ts HLS (mpegts) into FLV and pipes it into the main
// ffmpeg's stdin, exactly like Rust dmlive's create_pre_ff_command.
func (f *FF) runPreFF(ctx context.Context, videoURL string) error {
	pre := exec.Command("ffmpeg",
		"-y", "-xerror", "-hide_banner", "-nostats",
		"-probesize", "204800",
		"-i", videoURL,
		"-map", "0:v:0?", "-map", "0:a:0?",
		"-c", "copy",
		"-f", "flv", "-",
	)
	preStdin, err := pre.StdinPipe()
	if err != nil {
		return err
	}
	preStdout, err := pre.StdoutPipe()
	if err != nil {
		return err
	}
	if err := pre.Start(); err != nil {
		return fmt.Errorf("ffmpegctl: spawn pre-ffmpeg: %w", err)
	}

	go io.Copy(f.stdin, preStdout) //nolint:errcheck — ff dying closes stdin, pre-ff gets EPIPE

	go func() {
		<-ctx.Done()
		_ = preStdin.Close()
	}()
	return nil
}

// parseStderr drives the video-info / output-ready callbacks from ffmpeg's
// log lines (Rust get_video_info).
func (f *FF) parseStderr(stderr io.ReadCloser) {
	vinfoSent := false
	ffreadySent := false

	sc := bufio.NewScanner(stderr)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if dmRe.MatchString(line) {
			if !ffreadySent {
				ffreadySent = true
				if f.OnOutputReady != nil {
					f.OnOutputReady()
				}
			}
			continue
		}
		if m := resRe.FindStringSubmatch(line); m != nil {
			w, _ := strconv.Atoi(m[1])
			h, _ := strconv.Atoi(m[2])
			if w < 100 || h < 100 {
				_ = f.Quit()
				continue
			}
			if !vinfoSent {
				vinfoSent = true
				if f.OnVideoInfo != nil {
					f.OnVideoInfo(w, h)
				}
			}
		}
	}
}

// Quit ends ffmpeg gracefully via its stdin, flushing the container.
func (f *FF) Quit() error {
	if f.stdin == nil {
		return fmt.Errorf("ffmpegctl: ffmpeg stdin not found")
	}
	_, err := io.WriteString(f.stdin, "q\n")
	return err
}

// Wait blocks until ffmpeg exits.
func (f *FF) Wait() error {
	if f.cmd == nil {
		return nil
	}
	return f.cmd.Wait()
}

// RecordTask runs the second ffmpeg that stores the relay output (Rust
// write_record_task). It is deliberately not bound to ctx: when the relay
// dies its connection EOFs and this ffmpeg finalizes the file on its own.
func RecordTask(f2mURL, title, filename string) (*exec.Cmd, error) {
	cmd := exec.Command("ffmpeg",
		"-y", "-hide_banner", "-nostdin",
		"-i", f2mURL,
		"-c", "copy", "-f", "matroska",
		filename,
	)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

// DanmakuOnlyTask stores just the danmaku ASS document (Rust
// write_danmaku_only_task, used by --download-dm).
func DanmakuOnlyTask(danmakuURL, filename string) (*exec.Cmd, error) {
	cmd := exec.Command("ffmpeg",
		"-y", "-hide_banner", "-nostdin",
		"-i", danmakuURL,
		"-c", "copy",
		filename,
	)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}
