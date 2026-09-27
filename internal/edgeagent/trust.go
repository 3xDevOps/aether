package edgeagent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/3xDevOps/Aether/internal/edgeproto"
)

// FetchEdgeKey asks the edge at edgeURL for the key it signs grants with.
func FetchEdgeKey(ctx context.Context, edgeURL string) (ed25519.PublicKey, error) {
	origin, err := edgeproto.Origin(edgeURL)
	if err != nil {
		return nil, fmt.Errorf("edgeagent: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+edgeproto.PathEdgeKey, nil)
	if err != nil {
		return nil, fmt.Errorf("edgeagent: %w", err)
	}
	req.Header.Set(edgeproto.HeaderVersion, strconv.Itoa(edgeproto.Version))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("edgeagent: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only body
	dec := json.NewDecoder(io.LimitReader(resp.Body, edgeproto.MaxRequestBodySize))
	if resp.StatusCode != http.StatusOK {
		var body edgeproto.ErrorBody
		_ = dec.Decode(&body)
		return nil, fmt.Errorf("edgeagent: GET %s%s: %s %s", origin, edgeproto.PathEdgeKey, resp.Status, body.Error)
	}
	var r edgeproto.EdgeKeyResponse
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("edgeagent: GET %s%s: %w", origin, edgeproto.PathEdgeKey, err)
	}
	if len(r.Key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("edgeagent: %s sent a %d-byte edge key, want %d", origin, len(r.Key), ed25519.PublicKeySize)
	}
	return r.Key, nil
}
