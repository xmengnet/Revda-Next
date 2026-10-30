package streamfinder

import "errors"

// Sentinel errors every finder returns for the conditions a user can act on.
// They give the player layer one thing to match on instead of the per-site
// wording each finder used to make up, so "streamer might be offline" from
// bilibili and "streamer is offline" from twitch arrive as the same fact.
var (
	// ErrOffline is the room existing but not streaming right now.
	ErrOffline = errors.New("streamer is offline")
	// ErrNotFound is the room code pointing at nothing.
	ErrNotFound = errors.New("room not found")
	// ErrGeoBlocked is the stream being unavailable in this region.
	ErrGeoBlocked = errors.New("stream geo blocked")
)

// Offline wraps a site's own reason into ErrOffline, so the cause survives
// for logs while callers match on the sentinel.
func Offline(reason string) error {
	return &OfflineError{Reason: reason}
}

// OfflineError is ErrOffline with the site's own explanation attached.
type OfflineError struct {
	Reason string
}

func (e *OfflineError) Error() string { return "streamer is offline: " + e.Reason }
func (e *OfflineError) Is(target error) bool {
	return target == ErrOffline
}

// Unwrap lets errors.Is(err, ErrOffline) match through the OfflineError.
func (e *OfflineError) Unwrap() error { return ErrOffline }
