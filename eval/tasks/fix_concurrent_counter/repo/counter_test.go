package counterx

import (
	"sync"
	"testing"
)

func TestCounter_Inc(t *testing.T) {
	c := &Counter{}
	for i := 0; i < 100; i++ {
		c.Inc()
	}
	if got := c.Value(); got != 100 {
		t.Errorf("Value() = %d, want 100", got)
	}
}

func TestCounter_Concurrent(t *testing.T) {
	c := &Counter{}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				c.Inc()
			}
		}()
	}
	wg.Wait()
	if got := c.Value(); got != 10000 {
		t.Errorf("concurrent Value() = %d, want 10000", got)
	}
}
