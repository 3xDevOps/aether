package browser

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"net"
	"net/http"
	"time"
)

// Client speaks HTTP exclusively over a private Unix socket. It neither opens
// TCP connections nor exposes Chromium's debugging pipe to the run container.
type Client struct {
	http      *http.Client
	transport *http.Transport
}

func NewClient(socketPath string) *Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		},
		MaxConnsPerHost:        24,
		MaxIdleConnsPerHost:    4,
		IdleConnTimeout:        30 * time.Second,
		ResponseHeaderTimeout:  35 * time.Second,
		MaxResponseHeaderBytes: 32 << 10,
	}
	return &Client{http: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("browser companion redirects are not allowed")
	}}, transport: transport}
}

func (c *Client) Close() { c.transport.CloseIdleConnections() }

func (c *Client) request(ctx context.Context, method, endpoint string, value any) (*http.Response, error) {
	var body io.Reader
	if value != nil {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		if len(encoded) > MaxRequestBytes {
			return nil, &Error{Code: "resource_limit", Message: "request exceeds byte limit"}
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://aether-browser"+endpoint, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("browser companion: %w", err)
	}
	if response.StatusCode == http.StatusOK {
		return response, nil
	}
	defer func() { _ = response.Body.Close() }()
	data, err := readBounded(response.Body, MaxMetadataBytes)
	if err != nil {
		return nil, err
	}
	var failure struct {
		Error *Error `json:"error"`
	}
	if json.Unmarshal(data, &failure) == nil && failure.Error != nil {
		return nil, failure.Error
	}
	return nil, fmt.Errorf("browser companion returned HTTP %d", response.StatusCode)
}

func readBounded(reader io.Reader, maximum int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, &Error{Code: "resource_limit", Message: "companion response exceeds byte limit"}
	}
	return data, nil
}

func (c *Client) json(ctx context.Context, method, endpoint string, value, target any) error {
	response, err := c.request(ctx, method, endpoint, value)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	data, err := readBounded(response.Body, MaxRequestBytes)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode browser response: %w", err)
	}
	return nil
}

func (c *Client) Health(ctx context.Context) (Health, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var health Health
	err := c.json(ctx, http.MethodGet, "/health", nil, &health)
	if err == nil && (health.ProtocolVersion != 1 || health.CreationKey == "" || health.ProcessID == "" || health.SessionID == "") {
		err = errors.New("browser companion returned incompatible identity or protocol")
	}
	return health, err
}

func (c *Client) Do(ctx context.Context, request Request) (Result, error) {
	if err := request.Validate(); err != nil {
		return Result{}, err
	}
	if request.Operation == "select_option" && request.Values == nil {
		request.Values = []string{}
	}
	timeout := 15 * time.Second
	if requested := time.Duration(request.TimeoutMS)*time.Millisecond + 5*time.Second; requested > timeout {
		timeout = requested
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var result Result
	err := c.json(ctx, http.MethodPost, "/command", request, &result)
	return result, err
}

func (c *Client) capture(ctx context.Context, endpoint string, request any) (Capture, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	response, err := c.request(ctx, http.MethodPost, endpoint, request)
	if err != nil {
		return Capture{}, err
	}
	defer func() { _ = response.Body.Close() }()
	encoded := response.Header.Get("X-Aether-Metadata")
	if len(encoded) > MaxMetadataBytes*4/3+4 {
		return Capture{}, errors.New("browser image metadata exceeds limit")
	}
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return Capture{}, fmt.Errorf("decode browser image metadata: %w", err)
	}
	var capture Capture
	if err = json.Unmarshal(data, &capture.Metadata); err != nil {
		return Capture{}, fmt.Errorf("decode browser image metadata: %w", err)
	}
	if capture.Metadata.ContentType != "image/png" || response.Header.Get("Content-Type") != "image/png" {
		return Capture{}, errors.New("browser returned unsupported image content type")
	}
	capture.Bytes, err = readBounded(response.Body, MaxImageBytes)
	if err != nil {
		return Capture{}, err
	}
	if !bytes.HasPrefix(capture.Bytes, []byte("\x89PNG\r\n\x1a\n")) {
		return Capture{}, errors.New("browser returned invalid PNG signature")
	}
	config, err := png.DecodeConfig(bytes.NewReader(capture.Bytes))
	if err != nil {
		return Capture{}, fmt.Errorf("decode browser PNG: %w", err)
	}
	if config.Width < 1 || config.Height < 1 || config.Width > 8192 || config.Height > 8192 || int64(config.Width)*int64(config.Height) > 32<<20 || config.Width != capture.Metadata.Width || config.Height != capture.Metadata.Height {
		return Capture{}, errors.New("browser image dimensions do not match bounded metadata")
	}
	return capture, nil
}

func (c *Client) Capture(ctx context.Context, request Request) (Capture, error) {
	if err := request.validateTarget(); err != nil {
		return Capture{}, err
	}
	capture, err := c.capture(ctx, "/capture", request)
	if err == nil && (capture.Metadata.SessionID != request.SessionID || capture.Metadata.PageID != request.PageID || capture.Metadata.PageRevision != request.PageRevision) {
		return Capture{}, errors.New("browser image target mismatch")
	}
	return capture, err
}
func (c *Client) RenderTerminal(ctx context.Context, snapshot TerminalSnapshot) (Capture, error) {
	if err := snapshot.Validate(); err != nil {
		return Capture{}, err
	}
	capture, err := c.capture(ctx, "/terminal", snapshot)
	if err == nil && (capture.Metadata.SessionID != snapshot.SessionID || capture.Metadata.ScreenRevision != snapshot.ScreenRevision || capture.Metadata.OutputPosition != snapshot.OutputPosition || capture.Metadata.Cols != snapshot.Cols || capture.Metadata.Rows != snapshot.Rows) {
		return Capture{}, errors.New("terminal image snapshot mismatch")
	}
	return capture, err
}

// Stream delivers binary JPEGs and metadata separately. The callback runs in
// this goroutine; a slow callback exerts backpressure, and the companion replaces
// its single pending frame. Cancellation closes the stream immediately. The
// caller must revalidate current authority before forwarding each frame.
func (c *Client) Stream(ctx context.Context, request Request, receive func(Capture) error) error {
	if err := request.validateTarget(); err != nil {
		return err
	}
	if receive == nil {
		return errors.New("browser frame receiver is required")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Long-running commands may wait for 30 seconds, but attaching a stream
	// still has its original short header deadline. Keep the body live after it.
	headerDeadline := time.AfterFunc(15*time.Second, cancel)
	response, err := c.request(ctx, http.MethodPost, "/stream", request)
	headerDeadline.Stop()
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.Header.Get("Content-Type") != "application/x-aether-browser-frames" {
		return errors.New("browser returned unsupported stream content type")
	}
	for {
		var prefix [8]byte
		if _, err := io.ReadFull(response.Body, prefix[:]); err != nil {
			return err
		}
		metadataSize := binary.BigEndian.Uint32(prefix[:4])
		imageSize := binary.BigEndian.Uint32(prefix[4:])
		if metadataSize == 0 || metadataSize > MaxMetadataBytes || imageSize == 0 || imageSize > MaxFrameBytes {
			return &Error{Code: "resource_limit", Message: "browser frame exceeds byte limit"}
		}
		metadata := make([]byte, metadataSize)
		if _, err := io.ReadFull(response.Body, metadata); err != nil {
			return err
		}
		var frame Capture
		if err := json.Unmarshal(metadata, &frame.Metadata); err != nil {
			return fmt.Errorf("decode browser frame: %w", err)
		}
		if frame.Metadata.ContentType != "image/jpeg" || frame.Metadata.SessionID != request.SessionID || frame.Metadata.PageID != request.PageID || frame.Metadata.ViewportID == "" {
			return errors.New("browser frame target or content type mismatch")
		}
		if !bounded(frame.Metadata.Width, 1, 2560) || !bounded(frame.Metadata.Height, 1, 1600) || frame.Metadata.PageRevision == 0 {
			return errors.New("browser frame geometry or revision is invalid")
		}
		frame.Bytes = make([]byte, imageSize)
		if _, err := io.ReadFull(response.Body, frame.Bytes); err != nil {
			return err
		}
		if err := receive(frame); err != nil {
			return err
		}
	}
}
