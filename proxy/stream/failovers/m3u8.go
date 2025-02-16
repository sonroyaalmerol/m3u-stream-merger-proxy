package failovers

import (
	"bufio"
	"fmt"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/proxy/client"
	"m3u-stream-merger/proxy/loadbalancer"
	"net/url"
)

type M3U8Processor struct {
	logger logger.Logger
}

func NewM3U8Processor(logger logger.Logger) *M3U8Processor {
	return &M3U8Processor{logger: logger}
}

func (p *M3U8Processor) ProcessM3U8Stream(
	lbResult *loadbalancer.LoadBalancerResult,
	streamClient *client.StreamClient,
) error {
	base, err := url.Parse(lbResult.Response.Request.URL.String())
	if err != nil {
		return err
	}

	reader := bufio.NewScanner(lbResult.Response.Body)
	contentType := lbResult.Response.Header.Get("Content-Type")

	streamClient.SetHeader("Content-Type", contentType)
	for reader.Scan() {
		line := reader.Text()
		if err := p.processLine(lbResult, line, streamClient, base); err != nil {
			return fmt.Errorf("process line error: %w", err)
		}
	}

	return nil
}

func (p *M3U8Processor) processLine(
	lbResult *loadbalancer.LoadBalancerResult,
	line string,
	streamClient *client.StreamClient,
	baseURL *url.URL,
) error {
	if len(line) == 0 {
		return nil
	}

	if line[0] == '#' {
		return p.writeLine(streamClient, line)
	}

	return p.processURL(lbResult, line, streamClient, baseURL)
}

func (p *M3U8Processor) processURL(
	lbResult *loadbalancer.LoadBalancerResult,
	line string,
	streamClient *client.StreamClient,
	baseURL *url.URL,
) error {
	u, err := url.Parse(line)
	if err != nil {
		p.logger.Errorf("Failed to parse M3U8 URL in line: %v", err)
		return p.writeLine(streamClient, line)
	}

	if !u.IsAbs() {
		u = baseURL.ResolveReference(u)
	}

	segment := M3U8Segment{
		URL:       u.String(),
		SourceM3U: lbResult.Index + "|" + lbResult.SubIndex,
	}

	return p.writeLine(streamClient, generateSegmentURL(&segment))
}

func (p *M3U8Processor) writeLine(streamClient *client.StreamClient, line string) error {
	_, err := streamClient.Write([]byte(line + "\n"))
	if err != nil {
		return fmt.Errorf("write line error: %w", err)
	}
	return nil
}
