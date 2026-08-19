package profile

import profilesvc "github.com/3xDevOps/Aether/internal/profile"

// Finding is one scanner hit in a candidate profile file.
type Finding struct {
	Path     string
	Location string
	Kind     string
}

func scanContent(rel string, content []byte) []Finding {
	hits := profilesvc.ScanContent(rel, content)
	out := make([]Finding, len(hits))
	for i, h := range hits {
		out[i] = Finding{Path: h.Path, Location: h.Location, Kind: h.Kind}
	}
	return out
}
