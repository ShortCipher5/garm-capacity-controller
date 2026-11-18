package hostbootstrap

import (
	"bytes"
	"errors"
)

type limitWriter struct {
	bytes.Buffer
	exceeded bool
}

func (w *limitWriter) Write(p []byte) (int, error) {
	if len(p) > 65536-w.Len() {
		w.exceeded = true
		return 0, errors.New("output too large")
	}
	return w.Buffer.Write(p)
}
