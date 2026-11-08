package pages

import (
	"net/http"

	"github.com/Ahhhh-man/registry-ui/internal/web"
)

// Repo serves a repository's page (#13).
func (s *Server) Repo(w http.ResponseWriter, r *http.Request, _ web.Ref) { notYet(w, r) }
