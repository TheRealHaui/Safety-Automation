package httpHandler

import (
	"errors"
	"log"
	"net/http"

	"github.com/TheRealHaui/Safety-Automation/internal/auth"
)

// ─── Auth Handlers ────────────────────────────────────────────────────────────

func (h *SharedHTTPRequestDependenciesHandlerStruct) PostLogin(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username")
	password := r.FormValue("password")

	ok, err := auth.Login(w, r, h.DB, username, password)
	if errors.Is(err, auth.ErrAccountDisabled) {
		h.render(w, r, "login_template", map[string]string{"Error": "Your account has been disabled. Please contact your administrator."})
		return
	}
	if err != nil {
		log.Printf("login error: %v", err)
		h.render(w, r, "login_template", map[string]string{"Error": "Login failed. Please try again."})
		return
	}
	if !ok {
		h.render(w, r, "login_template", map[string]string{"Error": "Invalid username or password."})
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *SharedHTTPRequestDependenciesHandlerStruct) GetLogin(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, "login_template", nil)
}

func (h *SharedHTTPRequestDependenciesHandlerStruct) PostLogout(w http.ResponseWriter, r *http.Request) {
	_ = auth.Logout(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
