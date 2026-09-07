package client

import (
	"net/http"
	"testing"
	"time"
)

type deadlineWriter struct {
	header   http.Header
	deadline time.Time
	body     []byte
}

func (w *deadlineWriter) Header() http.Header {
	return w.header
}

func (w *deadlineWriter) Write(data []byte) (int, error) {
	w.body = append(w.body, data...)
	return len(data), nil
}

func (w *deadlineWriter) WriteHeader(int) {}

func (w *deadlineWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	return nil
}

func TestStreamClientWriteSetsDeadline(t *testing.T) {
	writer := &deadlineWriter{header: make(http.Header)}
	streamClient := NewStreamClient(writer, nil)
	before := time.Now().Add(clientWriteDeadline)

	if _, err := streamClient.Write([]byte("stream")); err != nil {
		t.Fatal(err)
	}

	after := time.Now().Add(clientWriteDeadline)
	if writer.deadline.Before(before) || writer.deadline.After(after) {
		t.Fatalf("deadline %v outside [%v, %v]", writer.deadline, before, after)
	}
	if string(writer.body) != "stream" {
		t.Fatalf("body = %q, want stream", writer.body)
	}
}
