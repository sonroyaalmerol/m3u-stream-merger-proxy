package client

import (
	"net/http"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

type StreamClient struct {
	ID              string
	Request         *http.Request
	StartedAt       time.Time
	ResponseHeaders http.Header
	HeadersSent     atomic.Bool
	respWriter      http.ResponseWriter
	flusher         http.Flusher
}

func NewStreamClient(w http.ResponseWriter, r *http.Request) *StreamClient {
	var flusher http.Flusher
	if f, ok := w.(http.Flusher); ok {
		flusher = f
	}

	return &StreamClient{
		ID:              uuid.New().String(),
		Request:         r,
		StartedAt:       time.Now(),
		ResponseHeaders: make(http.Header),
		respWriter:      w,
		flusher:         flusher,
	}
}

func (sc *StreamClient) SetHeader(key, value string) {
	if sc.HeadersSent.Load() {
		// Too late to modify headers.
		return
	}
	sc.ResponseHeaders.Set(key, value)
}

func (sc *StreamClient) Header() http.Header {
	if sc.HeadersSent.Load() {
		return sc.respWriter.Header()
	}
	return sc.ResponseHeaders
}

func (sc *StreamClient) WriteHeader(statusCode int) error {
	if sc.HeadersSent.Load() {
		// WriteHeader should only be called once.
		return nil
	}

	for key, values := range sc.ResponseHeaders {
		for _, value := range values {
			sc.respWriter.Header().Add(key, value)
		}
	}

	sc.respWriter.WriteHeader(statusCode)
	sc.HeadersSent.Store(true)
	return nil
}

func (sc *StreamClient) IsWritable() bool {
	return sc.respWriter != nil
}

func (sc *StreamClient) Write(data []byte) (int, error) {
	if !sc.HeadersSent.Load() {
		_ = sc.WriteHeader(http.StatusOK)
	}
	return sc.respWriter.Write(data)
}

func (sc *StreamClient) Flush() {
	if sc.flusher != nil {
		sc.flusher.Flush()
	}
}
