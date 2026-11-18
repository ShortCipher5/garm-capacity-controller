package garmapi

import (
	"context"
	"sync"
)

// managementMutex keeps the same single serialization domain for reads and
// writes, but permits a queued operation to withdraw without a waiting goroutine.
// Like sync.Mutex it must not be copied after first use. Lock remains available for existing callers; cancellation never unlocks another
// operation.
type managementMutex struct {
	once sync.Once
	gate chan struct{}
}

func (m *managementMutex) init() { m.once.Do(func() { m.gate = make(chan struct{}, 1) }) }
func (m *managementMutex) Lock() { m.init(); m.gate <- struct{}{} }
func (m *managementMutex) Unlock() {
	select {
	case <-m.gate:
	default:
		panic("unlock of unlocked management mutex")
	}
}
func (m *managementMutex) lockContext(ctx context.Context) bool {
	if ctx == nil || ctx.Err() != nil {
		return false
	}
	m.init()
	select {
	case m.gate <- struct{}{}:
	case <-ctx.Done():
		return false
	}
	// Both branches can be ready together. Never start a canceled queued read.
	if ctx.Err() != nil {
		m.Unlock()
		return false
	}
	return true
}
