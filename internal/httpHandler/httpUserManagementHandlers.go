package httpHandler

import (
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/TheRealHaui/Safety-Automation/internal/auth"
	"github.com/TheRealHaui/Safety-Automation/internal/backend/database"
	"github.com/go-chi/chi/v5"
)

// GetUsers renders the user list page, optionally filtered by ?search=<username>.
// Flash messages ?created=1 and ?updated=1 are shown after a successful create/edit redirect.
func (h *SharedHTTPRequestDependenciesHandlerStruct) GetUsers(w http.ResponseWriter, r *http.Request) {
	search := r.URL.Query().Get("search")
	users, err := database.ListUsers(r.Context(), h.DB, search)
	if err != nil {
		log.Printf("users: ListUsers: %v", err)
		http.Error(w, "Could not load users.", http.StatusInternalServerError)
		return
	}

	var flash string
	switch {
	case r.URL.Query().Get("created") == "1":
		flash = "User created successfully."
	case r.URL.Query().Get("updated") == "1":
		flash = "User updated successfully."
	}

	data := map[string]any{
		"Users":   users,
		"Search":  search,
		"Success": flash,
	}

	// htmx search: serve only the table partial so the rest of the page stays intact.
	if r.Header.Get("HX-Request") == "true" {
		h.partial(w, "users_table_template", data)
		return
	}

	h.render(w, r, "users_template", data)
}

// GetNewUser renders the create-user form.
func (h *SharedHTTPRequestDependenciesHandlerStruct) GetNewUser(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, "user_form_template", map[string]any{
		"IsNew": true,
	})
}

// PostCreateUser handles the create-user form submission.
func (h *SharedHTTPRequestDependenciesHandlerStruct) PostCreateUser(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	confirm := r.FormValue("confirm_password")
	isAdmin := r.FormValue("is_admin") == "on"
	isEnabled := r.FormValue("is_enabled") == "on"

	renderErr := func(msg string) {
		h.render(w, r, "user_form_template", map[string]any{
			"IsNew":         true,
			"Error":         msg,
			"FormUsername":  username,
			"FormIsAdmin":   isAdmin,
			"FormIsEnabled": isEnabled,
		})
	}

	if username == "" {
		renderErr("Username is required.")
		return
	}
	if len(username) > 100 {
		renderErr("Username must not exceed 100 characters.")
		return
	}
	if password != confirm {
		renderErr("Passwords do not match.")
		return
	}
	if msg := validatePassword(password, username); msg != "" {
		renderErr(msg)
		return
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		log.Printf("users: HashPassword: %v", err)
		renderErr("Could not process the password. Please try again.")
		return
	}

	if err := database.CreateUser(r.Context(), h.DB, username, hash, isAdmin, isEnabled); err != nil {
		// ORA-00001 = unique constraint violation — username already taken.
		if strings.Contains(err.Error(), "ORA-00001") {
			renderErr("A user with that username already exists.")
			return
		}
		log.Printf("users: CreateUser(%q): %v", username, err)
		renderErr("Could not create the user. Please try again.")
		return
	}

	http.Redirect(w, r, "/admin/users?created=1", http.StatusSeeOther)
}

// GetEditUser renders the edit form for an existing user.
func (h *SharedHTTPRequestDependenciesHandlerStruct) GetEditUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	targetUser, err := database.GetUserByID(r.Context(), h.DB, id)
	if err != nil {
		log.Printf("users: GetUserByID(%d): %v", id, err)
		http.NotFound(w, r)
		return
	}
	h.render(w, r, "user_form_template", map[string]any{
		"IsNew":      false,
		"TargetUser": targetUser,
	})
}

// PostUpdateUser handles the edit form submission for an existing user.
func (h *SharedHTTPRequestDependenciesHandlerStruct) PostUpdateUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	targetUser, err := database.GetUserByID(r.Context(), h.DB, id)
	if err != nil {
		log.Printf("users: GetUserByID(%d): %v", id, err)
		http.NotFound(w, r)
		return
	}

	newIsAdmin := r.FormValue("is_admin") == "on"
	newIsEnabled := r.FormValue("is_enabled") == "on"
	newPassword := r.FormValue("new_password")
	confirmPassword := r.FormValue("confirm_password")

	renderErr := func(msg string) {
		h.render(w, r, "user_form_template", map[string]any{
			"IsNew":      false,
			"TargetUser": targetUser,
			"Error":      msg,
		})
	}

	// Validate password before touching the database.
	var newHash string
	if newPassword != "" {
		if newPassword != confirmPassword {
			renderErr("Passwords do not match.")
			return
		}
		if msg := validatePassword(newPassword, targetUser.Username); msg != "" {
			renderErr(msg)
			return
		}
		newHash, err = auth.HashPassword(newPassword)
		if err != nil {
			log.Printf("users: HashPassword: %v", err)
			renderErr("Could not process the new password. Please try again.")
			return
		}
	}

	// Safety gate: refuse any change that would leave zero active admins.
	// The check only applies when the target user is currently an active admin
	// and the proposed change would remove their admin or enabled status.
	if targetUser.IsAdmin && targetUser.IsEnabled && (!newIsAdmin || !newIsEnabled) {
		count, err := database.CountActiveAdmins(r.Context(), h.DB)
		if err != nil {
			log.Printf("users: CountActiveAdmins: %v", err)
			renderErr("Could not verify admin count. Please try again.")
			return
		}
		if count <= 1 {
			renderErr("Cannot remove admin access or disable this account: it is the only active administrator. Promote another user to admin first.")
			return
		}
	}

	if err := database.UpdateUserFlags(r.Context(), h.DB, id, newIsAdmin, newIsEnabled); err != nil {
		log.Printf("users: UpdateUserFlags(%d): %v", id, err)
		renderErr("Could not update the user. Please try again.")
		return
	}

	if newHash != "" {
		if err := database.UpdateUserPassword(r.Context(), h.DB, id, newHash); err != nil {
			log.Printf("users: UpdateUserPassword(%d): %v", id, err)
			renderErr("Flags saved, but the password could not be updated. Please try again.")
			return
		}
	}

	http.Redirect(w, r, "/admin/users?updated=1", http.StatusSeeOther)
}
