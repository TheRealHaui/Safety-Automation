package httpHandler

import "net/http"

func (h *SharedHTTPRequestDependenciesHandlerStruct) GetAdminLogs(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, "admin_logs_template", h.ServerLog.Lines())
}
