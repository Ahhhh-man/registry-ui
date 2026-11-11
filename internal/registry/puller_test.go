package registry

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// Pullers are dropped, least recently used first, once they remember too
// many repositories between them.
func TestPullersBounded(t *testing.T) {
	var pings atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			pings.Add(1)
			return
		}
		w.WriteHeader(http.StatusNotFound) // no tags
	}))
	t.Cleanup(srv.Close)
	a, err := NewAPI(srv.URL, false)
	if err != nil {
		t.Fatal(err)
	}
	a.max = 2

	for i, step := range []struct {
		user, repo string
		pings      int32 // 1 when the registry has to be asked how to sign in
	}{
		{"alice", "a", 1},
		{"bob", "b", 1},
		{"alice", "a", 0}, // remembered
		{"carol", "c", 1}, // one too many, so bob's goes: alice's was used since
		{"alice", "a", 0},
		{"bob", "b", 1},
		{"bob", "b", 0},
		{"bob", "b2", 1}, // bob's now remembers two, so alice's goes
		{"bob", "b3", 1}, // too many for bob's alone: it serves this call and goes
		{"bob", "b", 1},
	} {
		before := pings.Load()
		if _, err := a.Tags(WithCredentials(t.Context(), step.user, "pass"), step.repo); err != nil {
			t.Fatal(err)
		}
		if got := pings.Load() - before; got != step.pings {
			t.Errorf("step %d, %s looks at %s: GET /v2/ sent %d times, want %d", i, step.user, step.repo, got, step.pings)
		}
	}
}
