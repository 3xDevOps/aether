package protocol

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"sync"
)

// Client is a minimal NDJSON JSON-RPC client over any byte stream
// (typically an SSH channel running the aether-control subsystem). Calls
// are serialized: one request, then its response, in order.
type Client struct {
	mu     sync.Mutex
	rwc    io.ReadWriteCloser
	r      *bufio.Reader
	nextID uint64
}

// NewClient wraps rwc in a control-channel client.
func NewClient(rwc io.ReadWriteCloser) *Client {
	return &Client{rwc: rwc, r: bufio.NewReaderSize(rwc, 64<<10)}
}

// Call performs one JSON-RPC request. params may be nil; result, when
// non-nil, receives the unmarshalled result object. A server-reported
// error is returned as *Error.
func (c *Client) Call(method string, params, result any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.nextID++
	req := Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(strconv.FormatUint(c.nextID, 10)),
		Method:  method,
	}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("protocol: marshal params: %w", err)
		}
		req.Params = raw
	}
	line, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("protocol: marshal request: %w", err)
	}
	if _, werr := c.rwc.Write(append(line, '\n')); werr != nil {
		return fmt.Errorf("protocol: write request: %w", werr)
	}

	respLine, err := ReadLine(c.r)
	if err != nil {
		return fmt.Errorf("protocol: read response: %w", err)
	}
	var resp Response
	if err := json.Unmarshal(respLine, &resp); err != nil {
		return fmt.Errorf("protocol: decode response: %w", err)
	}
	if string(resp.ID) != string(req.ID) {
		return fmt.Errorf("protocol: response id %s does not match request id %s", resp.ID, req.ID)
	}
	if resp.Error != nil {
		return resp.Error
	}
	if result != nil {
		if err := json.Unmarshal(resp.Result, result); err != nil {
			return fmt.Errorf("protocol: decode result: %w", err)
		}
	}
	return nil
}

// Close closes the underlying stream.
func (c *Client) Close() error { return c.rwc.Close() }

// ReadLine reads one newline-terminated NDJSON frame from r, enforcing
// MaxLineBytes; the trailing newline is stripped.
func ReadLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > MaxLineBytes {
			return nil, fmt.Errorf("protocol: line exceeds %d bytes", MaxLineBytes)
		}
		switch err {
		case nil:
			return line[:len(line)-1], nil
		case bufio.ErrBufferFull:
			continue
		default:
			return nil, err
		}
	}
}
