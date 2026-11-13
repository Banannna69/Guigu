package server

import (
	"log"
	"os"
	"testing"
	"time"
)

// Reproduces: logSinkT.Write is called by the log package with its internal
// mutex held. Any log.Printf from inside Write deadlocks.
func TestLogSinkRecursiveLogDeadlock(t *testing.T) {
	s := &logSinkT{cap: 10, subs: map[chan LogLine]struct{}{}, out: os.Stderr}
	// Simulate a full DB write queue so add() takes the "dropped" branch.
	ch := make(chan dbWriteReq, 0)
	s.dbCh = ch
	log.SetOutput(s)
	defer log.SetOutput(os.Stderr)

	done := make(chan struct{})
	go func() {
		log.Printf("hello") // -> Write -> add -> dropped -> log.Printf -> deadlock
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("DEADLOCK: log.Printf inside logSinkT.Write never returned")
	}
}
