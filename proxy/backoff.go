package proxy

import (
	"context"
	"time"
)

type BackoffStrategy struct {
	initial time.Duration
	max     time.Duration
	current time.Duration
}

func NewBackoffStrategy(initial, max time.Duration) *BackoffStrategy {
	return &BackoffStrategy{
		initial: initial,
		max:     max,
		current: initial,
	}
}

func (b *BackoffStrategy) Next() time.Duration {
	if b.max == 0 {
		return b.initial
	}

	current := b.current
	b.current *= 2
	if b.current > b.max {
		b.current = b.max
	}
	return current
}

func (b *BackoffStrategy) Sleep(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(b.Next()):
	}
}

func (b *BackoffStrategy) Reset() {
	if b.max > 0 {
		b.current = b.initial
	}
}
