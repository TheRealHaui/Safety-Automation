package httpHandler

import (
	"log"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/TheRealHaui/Safety-Automation/internal/auth"
	"github.com/TheRealHaui/Safety-Automation/internal/backend/database"
)

// GetProfile renders the user profile page showing last-login time and a
// password change form.
func (h *SharedHTTPRequestDependenciesHandlerStruct) GetProfile(w http.ResponseWriter, r *http.Request) {
	sessionUser := auth.UserFromContext(r.Context())

	// Load the full user record so we have LAST_LOGIN_AT.
	user, err := database.GetUserByID(r.Context(), h.DB, sessionUser.ID)
	if err != nil {
		log.Printf("profile: GetUserByID(%d): %v", sessionUser.ID, err)
		http.Error(w, "could not load profile", http.StatusInternalServerError)
		return
	}

	h.render(w, r, "profile_template", map[string]any{
		"ProfileUser": user,
	})
}

// PostChangePassword handles the password change form submission.
func (h *SharedHTTPRequestDependenciesHandlerStruct) PostChangePassword(w http.ResponseWriter, r *http.Request) {
	sessionUser := auth.UserFromContext(r.Context())

	currentPassword := r.FormValue("current_password")
	newPassword := r.FormValue("new_password")
	confirmPassword := r.FormValue("confirm_password")

	// Load full user record to verify the current password hash.
	// On DB error, user is nil — use http.Error rather than renderProfileWithError
	// to avoid a nil pointer dereference in the template.
	user, err := database.GetUserByID(r.Context(), h.DB, sessionUser.ID)
	if err != nil {
		log.Printf("profile: GetUserByID(%d): %v", sessionUser.ID, err)
		http.Error(w, "Could not load your profile. Please try again.", http.StatusInternalServerError)
		return
	}

	// Verify current password first so an attacker who hijacks an unlocked
	// session still needs to know the existing password to change it.
	if err := auth.CheckPassword(user.PasswordHash, currentPassword); err != nil {
		h.renderProfileWithError(w, r, user, "Current password is incorrect.")
		return
	}

	if newPassword != confirmPassword {
		h.renderProfileWithError(w, r, user, "New password and confirmation do not match.")
		return
	}

	if msg := validatePassword(newPassword, user.Username); msg != "" {
		h.renderProfileWithError(w, r, user, msg)
		return
	}

	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		log.Printf("profile: HashPassword: %v", err)
		h.renderProfileWithError(w, r, user, "Could not process the new password. Please try again.")
		return
	}

	if err := database.UpdateUserPassword(r.Context(), h.DB, user.ID, hash); err != nil {
		log.Printf("profile: UpdateUserPassword(%d): %v", user.ID, err)
		h.renderProfileWithError(w, r, user, "Could not save the new password. Please try again.")
		return
	}

	h.render(w, r, "profile_template", map[string]any{
		"ProfileUser": user,
		"Success":     "Password changed successfully.",
	})
}

func (h *SharedHTTPRequestDependenciesHandlerStruct) renderProfileWithError(w http.ResponseWriter, r *http.Request, user *database.User, msg string) {
	h.render(w, r, "profile_template", map[string]any{
		"ProfileUser": user,
		"Error":       msg,
	})
}

// validatePassword enforces the standard modern password policy:
//   - Minimum 12 characters
//   - At least one uppercase letter
//   - At least one lowercase letter
//   - At least one digit
//   - At least one special character (!@#$%^&*…)
//   - Must not contain the username (case-insensitive)
//
// Returns an empty string when the password is acceptable, or a
// human-readable error message when it is not.
func validatePassword(password, username string) string {
	// Check byte length before rune count — bcrypt silently truncates at 72 bytes,
	// and passing an unbounded string to bcrypt is a DoS vector (bcrypt is expensive).
	// 200 bytes: well above any legitimate password, well within bcrypt's safe range
	// once we reject beyond it, and keeps the meaningful key space honest.
	if len(password) > 200 {
		return "Password must not exceed 200 characters."
	}

	if utf8.RuneCountInString(password) < 12 {
		return "Password must be at least 12 characters long."
	}

	var hasUpper, hasLower, hasDigit, hasSpecial bool
	for _, ch := range password {
		switch {
		case unicode.IsUpper(ch):
			hasUpper = true
		case unicode.IsLower(ch):
			hasLower = true
		case unicode.IsDigit(ch):
			hasDigit = true
		case unicode.IsPunct(ch) || unicode.IsSymbol(ch):
			hasSpecial = true
		}
	}

	if !hasUpper {
		return "Password must contain at least one uppercase letter."
	}
	if !hasLower {
		return "Password must contain at least one lowercase letter."
	}
	if !hasDigit {
		return "Password must contain at least one number."
	}
	if !hasSpecial {
		return "Password must contain at least one special character (e.g. ! @ # $ % ^ & *)."
	}

	// Case-insensitive check: the password must not contain the username as a
	// substring. This is a common policy to prevent trivially personalised passwords.
	if strings.Contains(strings.ToLower(password), strings.ToLower(username)) {
		return "Password must not contain your username."
	}

	return ""
}
