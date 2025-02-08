package buffer

import (
	"context"
	"fmt"
	"io"
	"m3u-stream-merger/proxy"
	"m3u-stream-merger/proxy/loadbalancer"
	"sync/atomic"
	"time"
)

func (c *StreamCoordinator) StartMediaWriter(ctx context.Context, lbResult *loadbalancer.LoadBalancerResult) {
	defer func() {
		c.LBResultOnWrite.Store(nil)
		if r := recover(); r != nil {
			c.logger.Errorf("Panic in StartMediaWriter: %v", r)
			c.writeError(fmt.Errorf("internal server error"), proxy.StatusServerError)
		}
	}()
	defer lbResult.Response.Body.Close()

	if !c.WriterActive.CompareAndSwap(false, true) {
		c.logger.Warn("Writer already active, aborting start")
		return
	}
	defer c.WriterActive.Store(false)

	c.LBResultOnWrite.Store(lbResult)
	c.logger.Debug("StartMediaWriter: Beginning read loop")

	buffer := make([]byte, c.config.ChunkSize)
	timeout := c.getTimeoutDuration()
	backoff := proxy.NewBackoffStrategy(c.config.InitialBackoff,
		time.Duration(c.config.TimeoutSeconds-1)*time.Second)

	c.cm.UpdateConcurrency(lbResult.Index, true)
	defer c.cm.UpdateConcurrency(lbResult.Index, false)

	lastSuccess := time.Now()
	lastErr := time.Now()
	zeroReads := 0

	for atomic.LoadInt32(&c.state) == stateActive {
		select {
		case <-ctx.Done():
			c.logger.Debug("StartMediaWriter: Context cancelled")
			c.writeError(ctx.Err(), proxy.StatusClientClosed)
			return
		default:
			if c.shouldTimeout(lastSuccess, timeout) {
				c.writeError(nil, proxy.StatusServerError)
				return
			}

			n, err := lbResult.Response.Body.Read(buffer)
			c.logger.Debugf("StartMediaWriter: Read %d bytes, err: %v", n, err)

			if n == 0 {
				zeroReads++
				if zeroReads > 10 {
					c.writeError(io.EOF, proxy.StatusEOF)
					return
				}
				time.Sleep(10 * time.Millisecond)
				continue
			}

			lastSuccess = time.Now()
			zeroReads = 0

			if err == io.EOF {
				if n > 0 {
					chunk := newChunkData()
					_, _ = chunk.Buffer.Write(buffer[:n])
					chunk.Timestamp = time.Now()
					if !c.Write(chunk) {
						chunk.Reset()
					}
				}
				c.writeError(io.EOF, proxy.StatusEOF)
				return
			}

			if err != nil {
				if c.shouldRetry(timeout) {
					backoff.Sleep(ctx)
					lastErr = time.Now()
					continue
				}
				c.writeError(err, proxy.StatusServerError)
				return
			}

			chunk := newChunkData()
			_, _ = chunk.Buffer.Write(buffer[:n])
			chunk.Timestamp = time.Now()
			if !c.Write(chunk) {
				chunk.Reset()
			}

			// Only reset the backoff if at least one second has passed
			if time.Since(lastErr) >= time.Second {
				backoff.Reset()
				lastErr = time.Now()
			}
		}
	}
}
