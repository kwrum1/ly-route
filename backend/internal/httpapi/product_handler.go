package httpapi

import (
	"net/http"

	controlapi "ly-route/backend/internal/api"
)

func (server *Server) handleProduct(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	writeJSON(w, http.StatusOK, controlapi.ProductProfile{
		Product:      server.profile.ID(),
		Capabilities: server.profile.Capabilities(),
	})
}
