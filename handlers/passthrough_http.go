package handlers

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"m3u-stream-merger/logger"
	"m3u-stream-merger/utils"
)

type PassthroughHTTPHandler struct {
	logger logger.Logger
	client *http.Client
}

func NewPassthroughHTTPHandler(logger logger.Logger) *PassthroughHTTPHandler {
	transport, ok := utils.HTTPClient.Transport.(*http.Transport)
	if !ok {
		transport = http.DefaultTransport.(*http.Transport)
	}
	transport = transport.Clone()
	transport.DialContext = dialPublicNetwork

	client := *utils.HTTPClient
	client.Transport = transport
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if err := validatePassthroughURL(req.URL); err != nil {
			return err
		}
		req.Header.Set("User-Agent", utils.GetEnv("USER_AGENT"))
		req.Header.Set("Accept", utils.GetEnv("HTTP_ACCEPT"))
		return nil
	}

	return &PassthroughHTTPHandler{logger: logger, client: &client}
}

func (h *PassthroughHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	const prefix = "/a/"
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !strings.HasPrefix(r.URL.Path, prefix) {
		h.logger.Error("Invalid URL path: missing " + prefix)
		http.Error(w, "Invalid URL provided", http.StatusBadRequest)
		return
	}

	encodedURL := r.URL.Path[len(prefix):]
	if encodedURL == "" {
		h.logger.Error("No encoded URL provided in the path")
		http.Error(w, "No URL provided", http.StatusBadRequest)
		return
	}

	originalURLBytes, err := base64.URLEncoding.DecodeString(encodedURL)
	if err != nil {
		h.logger.Error("Failed to decode original URL: " + err.Error())
		http.Error(w, "Failed to decode original URL", http.StatusBadRequest)
		return
	}

	target, err := url.Parse(string(originalURLBytes))
	if err != nil || validatePassthroughURL(target) != nil {
		http.Error(w, "Invalid URL provided", http.StatusBadRequest)
		return
	}

	proxyReq, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), nil)
	if err != nil {
		h.logger.Error("Failed to create new request: " + err.Error())
		http.Error(w, "Error creating request", http.StatusInternalServerError)
		return
	}
	proxyReq.Header.Set("User-Agent", utils.GetEnv("USER_AGENT"))
	proxyReq.Header.Set("Accept", utils.GetEnv("HTTP_ACCEPT"))

	resp, err := h.client.Do(proxyReq)
	if err != nil {
		h.logger.Error("Failed to fetch original URL: " + err.Error())
		http.Error(w, "Error fetching the requested resource", http.StatusBadGateway)
		return
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			h.logger.Error("Failed to close response body: " + err.Error())
		}
	}()

	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	w.WriteHeader(resp.StatusCode)

	if _, err := io.Copy(w, resp.Body); err != nil {
		h.logger.Error("Failed to write response body: " + err.Error())
	}
}

func validatePassthroughURL(target *url.URL) error {
	if target == nil || (target.Scheme != "http" && target.Scheme != "https") || target.Hostname() == "" {
		return errors.New("only HTTP and HTTPS URLs are allowed")
	}
	if target.User != nil {
		return errors.New("URL credentials are not allowed")
	}
	if address, err := netip.ParseAddr(target.Hostname()); err == nil && !isPublicAddress(address) {
		return errors.New("private network URLs are not allowed")
	}
	return nil
}

func isPublicAddress(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsValid() || !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() {
		return false
	}
	if address.Is4() {
		carrierGradeNAT := netip.MustParsePrefix("100.64.0.0/10")
		return !carrierGradeNAT.Contains(address)
	}
	return true
}

func dialPublicNetwork(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("parse target address: %w", err)
	}

	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolve target host: %w", err)
	}
	if len(addresses) == 0 {
		return nil, errors.New("target host resolved to no addresses")
	}
	for _, resolved := range addresses {
		if !isPublicAddress(resolved) {
			return nil, errors.New("target host resolves to a private network")
		}
	}

	dialer := net.Dialer{}
	var dialErrors []error
	for _, resolved := range addresses {
		connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(resolved.String(), port))
		if err == nil {
			return connection, nil
		}
		dialErrors = append(dialErrors, err)
	}
	return nil, fmt.Errorf("dial target host: %w", errors.Join(dialErrors...))
}
