package buffer

import (
	"context"
	"fmt"
	"io"
	"m3u-stream-merger/proxy"
	"m3u-stream-merger/proxy/loadbalancer"
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

	c.LBResultOnWrite.Store(lbResult)
	c.WriterRespHeader.Store(nil)
	c.respHeaderSet = make(chan struct{})

	c.logger.Debug("StartMediaWriter: Beginning read loop")

	c.cm.UpdateConcurrency(lbResult.Index, true)
	defer c.cm.UpdateConcurrency(lbResult.Index, false)

	c.WriterRespHeader.Store(&lbResult.Response.Header)
	close(c.respHeaderSet)

	err := c.readAndWriteStream(ctx, lbResult.Response.Body, func(b []byte) error {
		chunk := newChunkData()
		_, _ = chunk.Buffer.Write(b)
		chunk.Timestamp = time.Now()
		if !c.Write(chunk) {
			chunk.Reset()
		}
		return nil
	})
	if err != nil {
		switch err {
		case ctx.Err():
			c.logger.Debug("StartWriter: Context cancelled")
			c.writeError(ctx.Err(), proxy.StatusClientClosed)
		case fmt.Errorf("stream timeout: no new segments"):
			c.writeError(nil, proxy.StatusServerError)
		case io.EOF:
			c.writeError(io.EOF, proxy.StatusEOF)
		default:
			c.writeError(err, proxy.StatusServerError)
		}
	}
}
