package relay

import (
	"bufio"
	"bytes"
	"errors"
	"net"
	"net/http"
)

// Keep ordinary JSON responses private until their accounting transaction has
// committed. Large or explicitly-flushed responses fall back to pass-through
// so buffering is bounded and binary endpoints remain usable.
const maxDeferredRelayResponseBytes = 8 << 20

var errDeferredResponseAlreadyDiscarded = errors.New("deferred relay response was discarded")

type deferredResponseWriter struct {
	underlying httpRelayResponseWriter
	snapshot   http.Header
	buffer     bytes.Buffer
	status     int
	size       int
	committed  bool
	discarded  bool
	err        error
}

// This local interface mirrors gin.ResponseWriter. Keeping it local makes the
// response deferral's required capabilities explicit and easy to test.
type httpRelayResponseWriter interface {
	http.ResponseWriter
	http.Hijacker
	http.Flusher
	http.CloseNotifier
	Status() int
	Size() int
	WriteString(string) (int, error)
	Written() bool
	WriteHeaderNow()
	Pusher() http.Pusher
}

func newDeferredResponseWriter(underlying httpRelayResponseWriter) *deferredResponseWriter {
	return &deferredResponseWriter{
		underlying: underlying,
		snapshot:   underlying.Header().Clone(),
		status:     http.StatusOK,
		size:       -1,
	}
}

func (w *deferredResponseWriter) Header() http.Header { return w.underlying.Header() }

func (w *deferredResponseWriter) WriteHeader(status int) {
	if status <= 0 || w.Written() {
		return
	}
	w.status = status
}

func (w *deferredResponseWriter) WriteHeaderNow() {
	if w.size < 0 {
		w.size = 0
	}
}

func (w *deferredResponseWriter) Write(data []byte) (int, error) {
	if w.discarded {
		return 0, errDeferredResponseAlreadyDiscarded
	}
	if w.err != nil {
		return 0, w.err
	}
	w.WriteHeaderNow()
	if w.committed {
		n, err := w.underlying.Write(data)
		w.size += n
		if err != nil {
			w.err = err
		}
		return n, err
	}
	if w.buffer.Len()+len(data) > maxDeferredRelayResponseBytes {
		if err := w.commitBuffered(); err != nil {
			return 0, err
		}
		n, err := w.underlying.Write(data)
		w.size += n
		if err != nil {
			w.err = err
		}
		return n, err
	}
	n, err := w.buffer.Write(data)
	w.size += n
	return n, err
}

func (w *deferredResponseWriter) WriteString(data string) (int, error) {
	return w.Write([]byte(data))
}

func (w *deferredResponseWriter) Status() int { return w.status }

func (w *deferredResponseWriter) Size() int { return w.size }

func (w *deferredResponseWriter) Written() bool { return w.size >= 0 }

func (w *deferredResponseWriter) Flush() {
	if err := w.Commit(); err == nil {
		w.underlying.Flush()
	}
}

func (w *deferredResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if err := w.Commit(); err != nil {
		return nil, nil, err
	}
	return w.underlying.Hijack()
}

func (w *deferredResponseWriter) CloseNotify() <-chan bool { return w.underlying.CloseNotify() }

func (w *deferredResponseWriter) Pusher() http.Pusher { return w.underlying.Pusher() }

func (w *deferredResponseWriter) Commit() error {
	if w.discarded {
		return errDeferredResponseAlreadyDiscarded
	}
	if w.err != nil {
		return w.err
	}
	if w.committed {
		return nil
	}
	return w.commitBuffered()
}

func (w *deferredResponseWriter) commitBuffered() error {
	w.underlying.WriteHeader(w.status)
	if w.buffer.Len() == 0 {
		w.underlying.WriteHeaderNow()
	} else {
		if _, err := w.underlying.Write(w.buffer.Bytes()); err != nil {
			w.err = err
			return err
		}
	}
	w.buffer.Reset()
	w.committed = true
	return nil
}

func (w *deferredResponseWriter) Reset() bool {
	if w.committed || w.discarded {
		return false
	}
	w.restoreHeaders()
	w.buffer.Reset()
	w.status = http.StatusOK
	w.size = -1
	w.err = nil
	return true
}

func (w *deferredResponseWriter) Discard() bool {
	if w.committed || w.discarded {
		return false
	}
	w.restoreHeaders()
	w.buffer.Reset()
	w.discarded = true
	w.size = -1
	return true
}

func (w *deferredResponseWriter) Committed() bool { return w.committed }

func (w *deferredResponseWriter) Err() error { return w.err }

func (w *deferredResponseWriter) restoreHeaders() {
	header := w.underlying.Header()
	for key := range header {
		delete(header, key)
	}
	for key, values := range w.snapshot {
		header[key] = append([]string(nil), values...)
	}
}
