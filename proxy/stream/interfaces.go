package stream

import (
	"net/http"
)

type ResponseWriter interface {
	http.ResponseWriter
}

type StreamFlusher interface {
	Flush()
}
