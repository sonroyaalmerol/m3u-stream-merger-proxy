package stream

import "time"

type StreamConfig struct {
	BufferSizeMB   int
	TimeoutSeconds int
	InitialBackoff time.Duration
}

func NewDefaultStreamConfig() *StreamConfig {
	return &StreamConfig{
		BufferSizeMB:   0,
		TimeoutSeconds: 3,
		InitialBackoff: 200 * time.Millisecond,
	}
}
