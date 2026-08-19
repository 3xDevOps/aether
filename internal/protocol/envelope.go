package protocol

import "encoding/json"

// ParseRequest decodes and validates one NDJSON request line and returns
// the response skeleton the transport answers with. Every transport that
// serves this JSON-RPC surface - the SSH control channel and the
// coordination socket - applies the same envelope rules, so they apply
// them from here: the caller's ID is echoed back when it sent a usable
// one, and ok is false when resp is already the complete answer.
func ParseRequest(line []byte) (req Request, resp Response, ok bool) {
	resp = Response{JSONRPC: "2.0", ID: json.RawMessage("null")}
	if err := json.Unmarshal(line, &req); err != nil {
		resp.Error = &Error{Code: CodeParse, Message: "parse error: " + err.Error()}
		return req, resp, false
	}
	if len(req.ID) != 0 && string(req.ID) != "null" {
		resp.ID = req.ID
	}
	if req.JSONRPC != "2.0" || req.Method == "" || len(req.ID) == 0 || string(req.ID) == "null" {
		resp.Error = &Error{Code: CodeInvalidRequest, Message: "invalid request"}
		return req, resp, false
	}
	return req, resp, true
}

// DecodeParams unmarshals a request's params under the rule both
// transports share: absent params are the zero value, not an error. The
// failure comes back bare, because the two word their CodeInvalidParams
// messages differently - the control channel by prefix, the coordination
// socket by method.
func DecodeParams[T any](raw json.RawMessage) (T, error) {
	var p T
	if len(raw) == 0 {
		return p, nil
	}
	err := json.Unmarshal(raw, &p)
	return p, err
}
