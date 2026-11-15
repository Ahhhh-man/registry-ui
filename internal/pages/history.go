package pages

import (
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/Ahhhh-man/registry-ui/internal/format"
	"github.com/Ahhhh-man/registry-ui/internal/registry"
	"github.com/Ahhhh-man/registry-ui/internal/web"
)

// historyView is the data for the "tag-history" template: one platform's
// layers, build steps and labels.
type historyView struct {
	Repo   string
	Layers []layerRow
	Size   int64 // all the layers, compressed
	Steps  []stepRow
	Labels [][2]string // key and value, sorted by key
}

type layerRow struct {
	Index  int // from 1
	Digest string
	Size   int64
	Bar    int    // the bar's length, in percent of the biggest layer's
	Step   string // the build step that made it, tidied; "" if unknown
}

type stepRow struct {
	Created time.Time
	Command string
	Empty   bool // made no layer
}

// history serves the "history" fragment of the image manifest digest: the
// tag page loads it for each platform only when it's opened.
func (s *Server) history(w http.ResponseWriter, r *http.Request, repo, digest string) {
	h, err := s.Src.History(r.Context(), repo, digest)
	if err != nil {
		web.Error(w, r, err)
		return
	}
	v := historyView{Repo: repo, Layers: make([]layerRow, len(h.Layers))}
	var biggest int64
	for _, l := range h.Layers {
		v.Size += l.Size
		biggest = max(biggest, l.Size)
	}
	for i, l := range h.Layers {
		v.Layers[i] = layerRow{Index: i + 1, Digest: l.Digest, Size: l.Size}
		if biggest > 0 {
			v.Layers[i].Bar = max(1, int(l.Size*100/biggest))
		}
	}
	// Steps that made a layer line up with the layers, in order. When they
	// don't add up, as when a builder records no history, none is guessed.
	var made []string
	for _, st := range h.Steps {
		cmd := format.Command(st.CreatedBy)
		v.Steps = append(v.Steps, stepRow{Created: st.Created, Command: cmd, Empty: st.Empty})
		if !st.Empty {
			made = append(made, cmd)
		}
	}
	if len(made) == len(v.Layers) {
		for i := range v.Layers {
			v.Layers[i].Step = made[i]
		}
	}
	for _, k := range slices.Sorted(maps.Keys(h.Labels)) {
		v.Labels = append(v.Labels, [2]string{k, h.Labels[k]})
	}
	s.fragment(w, r, "tag-history", v)
}

// historyOpen is the digest of the platform whose history opens with the
// page: the one ?platform= names, else the main one.
func historyOpen(m registry.Manifest, want string) string {
	for _, p := range m.Platforms {
		if !p.Missing && format.Platform(p) == want {
			return p.Digest
		}
	}
	p, _ := mainPlatform(m)
	return p.Digest
}
