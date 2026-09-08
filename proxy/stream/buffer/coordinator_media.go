package buffer

import (
	"context"
	"fmt"
	"io"
	"m3u-stream-merger/proxy"
	"m3u-stream-merger/proxy/loadbalancer"
)

func (c *StreamCoordinator) StartMediaWriter(ctx context.Context, lbResult *loadbalancer.LoadBalancerResult) {
	defer func() {
		c.LBResultOnWrite.Store(nil)
		if r := recover(); r != nil {
			c.logger.Errorf("Panic in StartMediaWriter: %v", r)
			c.writeError(fmt.Errorf("internal server error"), proxy.StatusServerError)
		}
	}()
	defer func() { _ = lbResult.Response.Body.Close() }()

	c.LBResultOnWrite.Store(lbResult)
	c.WriterRespHeader.Store(nil)
	c.resetHeaderChan()

	c.logger.Debug("StartMediaWriter: Beginning read loop")

	if !c.cm.UpdateConcurrency(lbResult.Index, true) {
		c.logger.Warnf("Failed to acquire concurrency slot for M3U_%s", lbResult.Index)
		c.writeError(fmt.Errorf("concurrency limit reached"), proxy.StatusServerError)
		return
	}
	defer c.cm.UpdateConcurrency(lbResult.Index, false)

	c.WriterRespHeader.Store(&lbResult.Response.Header)
	c.signalHeaderChan()

	var pacer *pcrPacer
	if c.config.EnablePCRPacer {
		pacer = newPCRPacer(int64(c.config.ChunkSize) * int64(c.config.SharedBufferSize))
	}
	err := c.readAndWriteStream(ctx, lbResult.Response.Body, func(b []byte) error {
		if err := pacer.pace(ctx, b); err != nil {
			return err
		}
		return c.writeChunk(b)
	})
	if err != nil {
		switch err {
		case ctx.Err():
			c.logger.Debug("StartWriter: Context cancelled")
			c.writeError(ctx.Err(), proxy.StatusClientClosed)
		case ErrStreamTimeout:
			c.writeError(nil, proxy.StatusServerError)
		case io.EOF:
			c.writeError(io.EOF, proxy.StatusEOF)
		default:
			c.writeError(err, proxy.StatusServerError)
		}
	}
}
