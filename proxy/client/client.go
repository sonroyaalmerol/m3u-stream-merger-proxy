package client

import (
	"net/http"
	"time"

	"github.com/google/uuid"
)

type StreamClient struct {
	ID              string
	Request         *http.Request
	StartedAt       time.Time
	ResponseHeaders http.Header
	HeadersSent     bool
	writer          http.ResponseWriter
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
		HeadersSent:     false,
		writer:          w,
		flusher:         flusher,
	}
}

func (sc *StreamClient) SetHeader(key, value string) {
	if sc.HeadersSent {
		// Too late to modify headers.
		return
	}
	sc.ResponseHeaders.Set(key, value)
}

func (sc *StreamClient) Header() http.Header {
	if sc.HeadersSent {
		return sc.writer.Header()
	}
	return sc.ResponseHeaders
}

func (sc *StreamClient) WriteHeader(statusCode int) error {
	if sc.HeadersSent {
		// WriteHeader should only be called once.
		return nil
	}

	for key, values := range sc.ResponseHeaders {
		for _, value := range values {
			sc.writer.Header().Add(key, value)
		}
	}

	sc.writer.WriteHeader(statusCode)
	sc.HeadersSent = true
	return nil
}

func (sc *StreamClient) GetWriter() http.ResponseWriter {
	return sc.writer
}

func (sc *StreamClient) IsWritable() bool {
	return sc.writer != nil
}

func (sc *StreamClient) Write(data []byte) (int, error) {
	if !sc.HeadersSent {
		_ = sc.WriteHeader(http.StatusOK)
	}
	return sc.writer.Write(data)
}

func (sc *StreamClient) Flush() {
	if sc.flusher != nil {
		sc.flusher.Flush()
	}
}
