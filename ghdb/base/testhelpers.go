package base

import "context"

// The functions below are for test use only. They expose internal engine state
// via type assertion so *baseDB fields stay unexported.

func FlushEngine(eng Engine, ctx context.Context) error {
	return eng.(*baseDB).flush(ctx)
}

func PollEngine(eng Engine, ctx context.Context) error {
	return eng.(*baseDB).poll(ctx)
}

func EngineInstanceID(eng Engine) string {
	return eng.(*baseDB).instanceID
}

func EngineWriteVer(eng Engine) int {
	b := eng.(*baseDB)
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.writeVer
}

func EngineNextVerExists(eng Engine) bool {
	b := eng.(*baseDB)
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.nextVerExists
}

func SetEngineNextVerExists(eng Engine, v bool) {
	b := eng.(*baseDB)
	b.mu.Lock()
	b.nextVerExists = v
	b.mu.Unlock()
}

// EngineWbufLen returns the current write-buffer length (for tests that verify mutations are buffered).
func EngineWbufLen(eng Engine) int {
	b := eng.(*baseDB)
	b.wbufMu.Lock()
	defer b.wbufMu.Unlock()
	return len(b.wbuf)
}

// EngineWbufOp returns the Op field of the i-th write-buffer entry, or "" if out of range.
func EngineWbufOp(eng Engine, i int) string {
	b := eng.(*baseDB)
	b.wbufMu.Lock()
	defer b.wbufMu.Unlock()
	if i >= len(b.wbuf) {
		return ""
	}
	return b.wbuf[i].Op
}
