package failovers

import (
	"bufio"
	"fmt"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/proxy/loadbalancer"
	"net/http"
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
	writer http.ResponseWriter,
) error {
	base, err := url.Parse(lbResult.Response.Request.URL.String())
	if err != nil {
		return err
	}

	reader := bufio.NewScanner(lbResult.Response.Body)
	contentType := lbResult.Response.Header.Get("Content-Type")

	writer.Header().Add("Content-Type", contentType)
	for reader.Scan() {
		line := reader.Text()
		if err := p.processLine(line, writer, base); err != nil {
			return fmt.Errorf("process line error: %w", err)
		}
	}

	return nil
}

func (p *M3U8Processor) processLine(
	line string,
	writer http.ResponseWriter,
	baseURL *url.URL,
) error {
	if len(line) == 0 {
		return nil
	}

	if line[0] == '#' {
		return p.writeLine(writer, line)
	}

	return p.processURL(line, writer, baseURL)
}

func (p *M3U8Processor) processURL(
	line string,
	writer http.ResponseWriter,
	baseURL *url.URL,
) error {
	u, err := url.Parse(line)
	if err != nil {
		p.logger.Errorf("Failed to parse M3U8 URL in line: %v", err)
		return p.writeLine(writer, line)
	}

	if !u.IsAbs() {
		u = baseURL.ResolveReference(u)
	}

	return p.writeLine(writer, u.String())
}

func (p *M3U8Processor) writeLine(writer http.ResponseWriter, line string) error {
	_, err := writer.Write([]byte(line + "\n"))
	if err != nil {
		return fmt.Errorf("write line error: %w", err)
	}
	return nil
}
