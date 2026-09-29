package edgeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// FetchEdgeInfo asks the edge at edgeURL, its relay origin, for the key it
// signs grants with and the rest of its EdgeInfo, validated. It is what
// that origin states; `aether-server edge trust` asks a person to compare
// the key with the fingerprint the edge's operator publishes.
func FetchEdgeInfo(ctx context.Context, edgeURL string) (edgeproto.EdgeInfo, error) {
	origin, err := edgeproto.Origin(edgeURL)
	if err != nil {
		return edgeproto.EdgeInfo{}, fmt.Errorf("edgeagent: %w", err)
	}
	url := origin + edgeproto.PathEdgeInfo
	ctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return edgeproto.EdgeInfo{}, fmt.Errorf("edgeagent: %w", err)
	}
	req.Header.Set(edgeproto.HeaderVersion, strconv.Itoa(edgeproto.Version))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return edgeproto.EdgeInfo{}, fmt.Errorf("edgeagent: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only body
	dec := json.NewDecoder(io.LimitReader(resp.Body, edgeproto.MaxRequestBodySize))
	if resp.StatusCode != http.StatusOK {
		var body edgeproto.ErrorBody
		_ = dec.Decode(&body)
		return edgeproto.EdgeInfo{}, fmt.Errorf("edgeagent: GET %s: %s %s", url, resp.Status, body.Error)
	}
	var info edgeproto.EdgeInfo
	if err := dec.Decode(&info); err != nil {
		return edgeproto.EdgeInfo{}, fmt.Errorf("edgeagent: GET %s: %w", url, err)
	}
	if err := info.Validate(); err != nil {
		return edgeproto.EdgeInfo{}, fmt.Errorf("edgeagent: GET %s: %w", url, err)
	}
	return info, nil
}
