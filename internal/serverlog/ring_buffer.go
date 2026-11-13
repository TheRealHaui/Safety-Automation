package serverlog

import (
	"strings"
	"sync"
)

const capacity = 500

// RingBuffer is a thread-safe, fixed-capacity ring buffer of log lines.
// It implements io.Writer so it can be passed to log.SetOutput via io.MultiWriter.
// The standard log package writes one complete formatted line per Write call,
// so typically each Write appends exactly one entry.
type RingBuffer struct {
	mu    sync.Mutex
	lines [capacity]string
	head  int // next write position; wraps modulo capacity
	count int // number of valid entries (0..capacity)
}

// Write implements io.Writer. Input is split on newlines; blank segments are discarded.
func (rb *RingBuffer) Write(p []byte) (int, error) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line == "" {
			continue
		}
		rb.lines[rb.head] = line
		rb.head = (rb.head + 1) % capacity
		if rb.count < capacity {
			rb.count++
		}
	}
	return len(p), nil
}

// Lines returns all buffered lines in chronological order (oldest first).
// Returns nil when the buffer is empty.
func (rb *RingBuffer) Lines() []string {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	if rb.count == 0 {
		return nil
	}
	out := make([]string, rb.count)
	if rb.count < capacity {
		// Buffer not yet full — valid entries are at indices 0..count-1.
		copy(out, rb.lines[:rb.count])
		return out
	}
	// Buffer full — oldest entry is at rb.head; iterate circularly.
	for i := 0; i < capacity; i++ {
		out[i] = rb.lines[(rb.head+i)%capacity]
	}
	return out
}
