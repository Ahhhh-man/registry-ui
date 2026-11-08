package pages

import (
	"net/http"

	"github.com/Ahhhh-man/registry-ui/internal/web"
)

// Catalog serves the repository list (#12).
func (s *Server) Catalog(w http.ResponseWriter, r *http.Request, _ web.Ref) { notYet(w, r) }
