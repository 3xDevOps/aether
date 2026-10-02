package edge

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	edgestore "github.com/3xDevOps/Aether/internal/edge/store"
)

const (
	deviceCodeTTL = 10 * time.Minute
	pollInterval  = 5 * time.Second
	// pollJitter tolerates polls that arrive a little early.
	pollJitter = time.Second
)

// api serves one JSON API route: it checks the client's protocol version,
// writes h's result as JSON, and writes a failure as edgeproto.ErrorBody
// with its status.
func (s *Service) api(h func(*http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hdr := w.Header()
		hdr.Set(edgeproto.HeaderVersion, strconv.Itoa(edgeproto.Version))
		hdr.Set("Cache-Control", "no-store")
		hdr.Set("X-Content-Type-Options", "nosniff")
		hdr.Set("Content-Type", "application/json")
		r.Body = http.MaxBytesReader(w, r.Body, edgeproto.MaxRequestBodySize)
		v, err := strconv.Atoi(r.Header.Get(edgeproto.HeaderVersion))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, edgeproto.ErrorBody{
				Error: fmt.Sprintf("missing or invalid %s header", edgeproto.HeaderVersion)})
			return
		}
		if err = edgeproto.CheckVersion(v); err != nil {
			writeJSON(w, http.StatusUpgradeRequired, edgeproto.ErrorBody{Error: err.Error()})
			return
		}
		out, err := h(r)
		if err != nil {
			if errors.Is(err, edgeproto.RefusalTokenRequired) || errors.Is(err, edgeproto.RefusalTokenRevoked) {
				hdr.Set("WWW-Authenticate", "Bearer")
			}
			status := errorStatus(err)
			logFailure(r, status, err)
			writeJSON(w, status, edgeproto.ErrorBody{Error: err.Error()})
			return
		}
		if out == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck // the client went away
}

func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		return pageErr(http.StatusBadRequest, "decode request: %v", err)
	}
	if dec.More() {
		return pageErr(http.StatusBadRequest, "decode request: more than one JSON value")
	}
	return nil
}

// deviceState is a device-flow poll answer: HTTP 400 with one of the
// edgeproto.Device* states.
func deviceState(state string) error {
	return &statusError{status: http.StatusBadRequest, msg: state}
}

func (s *Service) apiDeviceStart(r *http.Request) (any, error) {
	if !s.startLimit.allow(addrKeys(r)...) {
		return nil, edgeproto.RefusalTooMany
	}
	var req edgeproto.DeviceStartRequest
	if err := decodeJSON(r, &req); err != nil {
		return nil, err
	}
	if err := req.Validate(); err != nil {
		return nil, pageErr(http.StatusBadRequest, "%v", err)
	}
	key, _ := edgeproto.ParseDeviceKey(req.Key) // Validate parsed it.
	deviceCode := edgeproto.NewToken()
	now := s.now()
	// A new user code rarely collides with a live one; three draws make
	// a failure negligible.
	for range 3 {
		userCode := newUserCode()
		code, _ := normalizeUserCode(userCode)
		err := s.store.CreateDeviceAuth(r.Context(), edgestore.DeviceAuth{
			CodeHash:     edgeproto.HashToken(deviceCode),
			UserCodeHash: edgeproto.HashToken(code),
			Label:        req.Label,
			Key:          edgeproto.DeviceKeyLine(key),
			ClientAddr:   clientAddr(r).String(),
			CreatedAt:    now,
			ExpiresAt:    now.Add(deviceCodeTTL),
		})
		if errors.Is(err, edgestore.ErrConflict) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return edgeproto.DeviceStartResponse{
			DeviceCode:      deviceCode,
			UserCode:        userCode,
			VerificationURI: s.signinOrigin + "/device",
			ExpiresIn:       int(deviceCodeTTL.Seconds()),
			Interval:        int(pollInterval.Seconds()),
		}, nil
	}
	return nil, errors.New("edge: could not draw an unused user code; try again")
}

func (s *Service) apiDeviceToken(r *http.Request) (any, error) {
	if !s.pollLimit.allow(addrKeys(r)...) {
		return nil, edgeproto.RefusalTooMany
	}
	var req edgeproto.DeviceTokenRequest
	if err := decodeJSON(r, &req); err != nil {
		return nil, err
	}
	if !edgeproto.ValidToken(req.DeviceCode) {
		return nil, pageErr(http.StatusBadRequest, "device code is malformed")
	}
	ctx := r.Context()
	hash := edgeproto.HashToken(req.DeviceCode)
	now := s.now()
	a, err := s.store.PollDeviceAuth(ctx, hash, now)
	if errors.Is(err, edgestore.ErrNotFound) {
		return nil, deviceState(edgeproto.DeviceExpired)
	}
	if err != nil {
		return nil, err
	}
	if !now.Before(a.ExpiresAt) || a.Status == edgestore.AuthDenied {
		if err = s.store.DeleteDeviceAuth(ctx, hash); err != nil {
			return nil, err
		}
		if a.Status == edgestore.AuthDenied {
			return nil, deviceState(edgeproto.DeviceDenied)
		}
		return nil, deviceState(edgeproto.DeviceExpired)
	}
	if a.Status == edgestore.AuthPending {
		if !a.LastPollAt.IsZero() && now.Sub(a.LastPollAt) < pollInterval-pollJitter {
			return nil, deviceState(edgeproto.DeviceSlowDown)
		}
		return nil, deviceState(edgeproto.DevicePending)
	}
	token := edgeproto.NewToken()
	d, err := s.store.RedeemDeviceAuth(ctx, hash, edgestore.Device{
		ID:        edgeproto.NewConnID(),
		TokenHash: edgeproto.HashToken(token),
	}, now)
	if errors.Is(err, edgestore.ErrNotFound) {
		return nil, deviceState(edgeproto.DeviceExpired)
	}
	if err != nil {
		return nil, err
	}
	return edgeproto.DeviceTokenResponse{
		Token:   token,
		Device:  edgeproto.Device{ID: d.ID, Label: d.Label, Key: d.Key},
		Account: d.Account,
	}, nil
}

func (s *Service) apiLogout(r *http.Request) (any, error) {
	c, accountID, err := s.authenticate(r)
	if err != nil {
		return nil, err
	}
	return nil, s.revokeDevice(r.Context(), accountID, c.Device.ID)
}

func (s *Service) apiServers(r *http.Request) (any, error) {
	c, _, err := s.authenticate(r)
	if err != nil {
		return nil, err
	}
	rows, err := s.servers(r.Context(), c.Account.Account)
	if err != nil {
		return nil, err
	}
	out := edgeproto.ServersResponse{Servers: []edgeproto.ServerInfo{}}
	for _, row := range rows {
		out.Servers = append(out.Servers, row.ServerInfo)
	}
	return out, nil
}

func (s *Service) apiEdgeInfo(*http.Request) (any, error) {
	key := s.EdgeKey()
	return edgeproto.EdgeInfo{
		SigninOrigin: s.signinOrigin,
		Key:          key,
		Fingerprint:  edgeproto.EdgeKeyFingerprint(key),
		Version:      edgeproto.Version,
		MinVersion:   edgeproto.MinVersion,
	}, nil
}
