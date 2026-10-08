package ui

import (
	"bytes"
	"sync"
	"testing"
)

// TestLockedWriterSerializesConcurrentWrites covers the hazard behind
// Options.Output.
//
// Bubble Tea writes to the program's output from its renderer goroutine and
// from its event loop, so a caller-supplied writer is written concurrently. A
// mutex is what makes an ordinary in-process writer safe there; without it the
// race detector reports the buffer's own fields, which is what
// TestRunWithOptionsStartsAndStops used to show under load.
func TestLockedWriterSerializesConcurrentWrites(t *testing.T) {
	var buffer bytes.Buffer
	writer := &lockedWriter{writer: &buffer}

	var group sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for line := 0; line < 200; line++ {
				if _, err := writer.Write([]byte("frame\n")); err != nil {
					t.Errorf("Write: %v", err)
					return
				}
			}
		}()
	}
	group.Wait()

	if got, want := buffer.Len(), 8*200*len("frame\n"); got != want {
		t.Errorf("buffer holds %d bytes, want %d: a write was lost to a race", got, want)
	}
}
