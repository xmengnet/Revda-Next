// Package mpvctl drives an external mpv process over its JSON IPC socket.
// It mirrors the Rust dmlive MpvControl: spawn mpv, register the dml:*
// keybinds, then exchange commands and events with the running player.
package mpvctl

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Event is one mpv IPC event.
type Event struct {
	Name string
	Args []string // populated for client-message events
	Raw  json.RawMessage
}

// Mpv is a live connection to one mpv instance.
type Mpv struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc

	mu        sync.Mutex
	conn      net.Conn
	nextID    uint64
	replies   map[uint64]chan json.RawMessage
	connected chan struct{} // closed on the first successful dial
	once      sync.Once

	events chan Event
	done   chan struct{}
}

// keybindLines registers the dml:* script messages, matching Rust dmlive's
// init_mpv_rpc (alt+r restart, alt+z/x font size, alt+i nick, alt+b/n page,
// alt+f fps cycling).
var keybindLines = []string{
	`{ "command": ["keybind", "alt+r", "script-message dml:r"] }`,
	`{ "command": ["keybind", "alt+z", "script-message dml:fsdown"] }`,
	`{ "command": ["keybind", "alt+x", "script-message dml:fsup"] }`,
	`{ "command": ["keybind", "alt+i", "script-message dml:nick"] }`,
	`{ "command": ["keybind", "alt+b", "script-message dml:back"] }`,
	`{ "command": ["keybind", "alt+n", "script-message dml:next"] }`,
	`{ "command": ["keybind", "alt+f", "script-message dml:fps"] }`,
}

// Spawn launches mpv with the given extra arguments and connects to its IPC
// socket. The returned Mpv stops once ctx is cancelled or mpv exits. The
// caller stays responsible for the media URL and headers it wants to pass.
func Spawn(ctx context.Context, args []string) (*Mpv, error) {
	socketPath := fmt.Sprintf("/tmp/dml-%d-%d-mpv", os.Getpid(), time.Now().UnixNano()%1e9)
	full := append([]string{
		"--loop=no",
		"--keep-open=no",
		"--idle=yes",
		"--player-operation-mode=pseudo-gui",
		"--sub-visibility=yes",
		"--sid=1",
		fmt.Sprintf("--input-ipc-server=%s", socketPath),
	}, args...)

	runCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(runCtx, "mpv", full...)
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("mpvctl: spawn mpv: %w", err)
	}

	if !waitForSocket(ctx, socketPath, 15*time.Second) {
		_ = cmd.Process.Kill()
		cancel()
		return nil, fmt.Errorf("mpvctl: mpv IPC socket %s never appeared", socketPath)
	}

	m := &Mpv{
		cmd:       cmd,
		cancel:    cancel,
		replies:   map[uint64]chan json.RawMessage{},
		connected: make(chan struct{}),
		events:    make(chan Event, 64),
		done:      make(chan struct{}),
	}

	go m.manage(ctx, socketPath)

	go func() {
		select {
		case <-ctx.Done():
		case <-m.done:
		}
		_ = m.cmd.Process.Kill()
		cancel()
	}()

	return m, nil
}

// manage owns the connection lifecycle: dial, serve, and re-dial after drops
// until the context ends. Keybinds are registered once, on the first conn.
func (m *Mpv) manage(ctx context.Context, socketPath string) {
	defer close(m.done)
	first := true
	for {
		if ctx.Err() != nil {
			return
		}
		conn, err := net.Dial("unix", socketPath)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(300 * time.Millisecond):
				continue
			}
		}
		m.mu.Lock()
		m.conn = conn
		m.mu.Unlock()
		if first {
			first = false
			m.once.Do(func() { close(m.connected) })
			_ = m.rawLines(keybindLines)
		}
		m.serveConn(conn)
		m.mu.Lock()
		if m.conn == conn {
			m.conn = nil
		}
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// serveConn reads one connection until it drops, dispatching replies to
// waiting calls and events to the Events channel.
func (m *Mpv) serveConn(conn net.Conn) {
	go func() {
		<-m.done
		_ = conn.Close()
	}()

	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var msg struct {
			Event     string          `json:"event"`
			RequestID uint64          `json:"request_id"`
			Error     string          `json:"error"`
			Data      json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
			continue
		}
		if msg.Event == "" {
			if ch := m.takeReply(msg.RequestID); ch != nil {
				if msg.Error != "success" && msg.Error != "" {
					ch <- nil
				} else {
					ch <- msg.Data
				}
				close(ch)
			}
			continue
		}
		ev := Event{Name: msg.Event, Raw: json.RawMessage(sc.Bytes())}
		if msg.Event == "client-message" {
			var cm struct {
				Args []string `json:"args"`
			}
			if json.Unmarshal(sc.Bytes(), &cm) == nil {
				ev.Args = cm.Args
			}
		}
		select {
		case m.events <- ev:
		default: // drop on backpressure; consumers re-poll properties
		}
	}
}

// Ready closes once the IPC connection is up (commands can be sent).
func (m *Mpv) Ready() <-chan struct{} {
	return m.connected
}

// Command runs one mpv command and waits for its reply.
func (m *Mpv) Command(command ...any) error {
	_, err := m.request(command)
	return err
}

// GetProperty returns the raw JSON value of an mpv property.
func (m *Mpv) GetProperty(prop string) (json.RawMessage, error) {
	return m.request([]any{"get_property", prop})
}

// GetPropertyFloat returns a numeric property.
func (m *Mpv) GetPropertyFloat(prop string) (float64, error) {
	data, err := m.GetProperty(prop)
	if err != nil {
		return 0, err
	}
	var v float64
	if err := json.Unmarshal(data, &v); err != nil {
		return 0, fmt.Errorf("mpvctl: property %s not numeric", prop)
	}
	return v, nil
}

// GetPropertyString returns a string property.
func (m *Mpv) GetPropertyString(prop string) (string, error) {
	data, err := m.GetProperty(prop)
	if err != nil {
		return "", err
	}
	var v string
	if err := json.Unmarshal(data, &v); err != nil {
		return "", fmt.Errorf("mpvctl: property %s not a string", prop)
	}
	return v, nil
}

// LoadFile points mpv at a new URL, optionally forcing the media title.
func (m *Mpv) LoadFile(url, title string) error {
	if err := m.Command("loadfile", url); err != nil {
		return err
	}
	if title != "" {
		return m.Command("set_property", "force-media-title", title)
	}
	return nil
}

// request sends one command over the current connection and waits for its
// reply, matched by request_id (mpv interleaves events on the same socket).
func (m *Mpv) request(command []any) (json.RawMessage, error) {
	select {
	case <-m.connected:
	default:
		return nil, fmt.Errorf("mpvctl: not connected")
	}

	m.mu.Lock()
	conn := m.conn
	if conn == nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("mpvctl: not connected")
	}
	m.nextID++
	id := m.nextID
	req, _ := json.Marshal(map[string]any{"command": command, "request_id": id})
	if _, err := conn.Write(append(req, '\n')); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	ch := make(chan json.RawMessage, 1)
	m.replies[id] = ch
	m.mu.Unlock()

	select {
	case data, ok := <-ch:
		if !ok || data == nil {
			return nil, fmt.Errorf("mpvctl: command %v failed", command)
		}
		return data, nil
	case <-time.After(5 * time.Second):
		m.mu.Lock()
		delete(m.replies, id)
		m.mu.Unlock()
		return nil, fmt.Errorf("mpvctl: no reply for %v", command)
	}
}

func (m *Mpv) takeReply(id uint64) chan json.RawMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	ch := m.replies[id]
	delete(m.replies, id)
	return ch
}

// rawLines writes each line as an independent newline-delimited IPC message.
func (m *Mpv) rawLines(lines []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conn == nil {
		return fmt.Errorf("mpvctl: not connected")
	}
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if _, err := m.conn.Write([]byte(strings.TrimSpace(l) + "\n")); err != nil {
			return err
		}
	}
	return nil
}

// Events returns the event stream. Events are dropped when the consumer
// lags; property state should be re-read rather than inferred from events.
func (m *Mpv) Events() <-chan Event {
	return m.events
}

// Wait blocks until mpv exits or ctx is cancelled.
func (m *Mpv) Wait(ctx context.Context) {
	select {
	case <-ctx.Done():
	case <-m.done:
	}
}

func waitForSocket(ctx context.Context, path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return false
		}
		if conn, err := net.DialTimeout("unix", path, 500*time.Millisecond); err == nil {
			_ = conn.Close()
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}
