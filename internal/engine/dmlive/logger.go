package dmlive

import (
	"log"
	"sync/atomic"
)

// Leveled logging matching Rust dmlive's --log-level: 1 debug, 2 info,
// 3 warn (default), 4 error. A message prints when its level >= threshold.
var logLevel atomic.Int64

func setLogLevel(l int) {
	if l < 1 || l > 4 {
		l = 3
	}
	logLevel.Store(int64(l))
}

func logDebugf(format string, args ...any) { logf(1, format, args...) }
func logInfof(format string, args ...any)  { logf(2, format, args...) }
func logWarnf(format string, args ...any)  { logf(3, format, args...) }
func logErrorf(format string, args ...any) { logf(4, format, args...) }

func logf(level int, format string, args ...any) {
	if int64(level) < logLevel.Load() {
		return
	}
	var prefix string
	switch level {
	case 1:
		prefix = "[DEBUG] "
	case 2:
		prefix = "[INFO] "
	case 3:
		prefix = "[WARN] "
	default:
		prefix = "[ERROR] "
	}
	log.Printf(prefix+format, args...)
}
