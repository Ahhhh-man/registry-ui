package pages

import (
	"net/http"

	"github.com/Ahhhh-man/registry-ui/internal/web"
)

// Tag serves a tag's detail page (#14).
func (s *Server) Tag(w http.ResponseWriter, r *http.Request, _ web.Ref) { notYet(w, r) }

// Digest serves a manifest by digest, on the same page as Tag (#14).
func (s *Server) Digest(w http.ResponseWriter, r *http.Request, _ web.Ref) { notYet(w, r) }
