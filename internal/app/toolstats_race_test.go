package app

import (
	"sync"
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
)

func TestToolStatsConcurrentReadsDuringEvents(t *testing.T) {
	application := &App{}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				_ = application.ToolStats()
			}
		}()
	}
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				application.emit(agent.Event{Kind: agent.EventToolEnd, ToolName: "tool", ToolOK: index%2 == 0})
			}
		}(i)
	}
	wg.Wait()
}
