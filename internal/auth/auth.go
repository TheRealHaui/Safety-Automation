package auth

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"

	"github.com/TheRealHaui/Safety-Automation/internal/backend/database"
	"github.com/gorilla/sessions"
	"golang.org/x/crypto/bcrypt"
)

// ErrAccountDisabled is returned by Login when credentials are correct but the
// account has IS_ENABLED = 'N'. The caller should surface a distinct message so
// the user knows to contact an administrator rather than blaming a wrong password.
var ErrAccountDisabled = errors.New("account disabled")

const sessionName = "rt_session"
const sessionUserKey = "user_id"
const sessionUsernameKey = "username"
const sessionIsAdminKey = "is_admin"

var store *sessions.CookieStore

// Init initialises the session store.
//
// authKey   — HMAC-SHA256 signing key; must be 32 or 64 bytes.
// encryptKey — AES encryption key; must be 16, 24, or 32 bytes.
//
// Two keys are required so that cookie contents are opaque to the browser
// (the IS_ADMIN flag and user ID would otherwise be visible in base64-decoded
// cookie data, even though HMAC prevents tampering with those values).
func Init(authKey, encryptKey string) {
	store = sessions.NewCookieStore([]byte(authKey), []byte(encryptKey))
	store.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   86400 * 7, // 7 days
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

// Login validates credentials and creates a session. Returns false if invalid.
func Login(w http.ResponseWriter, r *http.Request, db *sql.DB, username, password string) (bool, error) {

	// Reject oversized passwords before bcrypt — bcrypt is intentionally expensive
	// and an unbounded input is a DoS vector. 200 bytes matches the cap in validatePassword.
	if len(password) > 200 {
		return false, nil
	}

	user, err := database.GetUserByUsername(r.Context(), db, username)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	if err := CheckPassword(user.PasswordHash, password); err != nil {
		return false, nil
	}

	// Checked after password verification — not before — so that a disabled-account
	// response is indistinguishable from a wrong-password response to anyone who
	// does not already know the correct password.
	if !user.IsEnabled {
		return false, ErrAccountDisabled
	}

	sess, _ := store.Get(r, sessionName)
	sess.Values[sessionUserKey] = user.ID
	sess.Values[sessionUsernameKey] = user.Username
	sess.Values[sessionIsAdminKey] = user.IsAdmin

	// Record the login timestamp. Failure is non-fatal — a missing timestamp is
	// cosmetic; blocking the login would be worse.
	if err := database.UpdateLastLoginAt(r.Context(), db, user.ID); err != nil {
		log.Printf("auth: could not update LAST_LOGIN_AT for user %d: %v", user.ID, err)
	}

	return true, sess.Save(r, w)
}

// Logout destroys the session.
func Logout(w http.ResponseWriter, r *http.Request) error {
	sess, _ := store.Get(r, sessionName)
	sess.Options.MaxAge = -1
	return sess.Save(r, w)
}

// GetSessionUser returns the logged-in user from the session, or nil.
func GetSessionUser(r *http.Request) *database.User {
	sess, err := store.Get(r, sessionName)
	if err != nil {
		return nil
	}
	id, ok1 := sess.Values[sessionUserKey].(int64)
	username, ok2 := sess.Values[sessionUsernameKey].(string)
	if !ok1 || !ok2 {
		return nil
	}
	// IsAdmin is stored as bool; the type assertion returns false if the key is
	// absent (e.g. sessions created before this field was added), which is the
	// correct safe default — treat unknown sessions as non-admin.
	isAdmin, _ := sess.Values[sessionIsAdminKey].(bool)
	return &database.User{ID: id, Username: username, IsAdmin: isAdmin}
}

type contextKey string

const userContextKey contextKey = "auth_user"

// Middleware enforces authentication on all routes except /login and /healthz.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /healthz and /readyz must be reachable without a session so that the Kubernetes
		// liveness and readiness probes work before (and independently of) any user login.
		if r.URL.Path == "/login" || r.URL.Path == "/login/" || r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		user := GetSessionUser(r)
		if user == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		ctx := context.WithValue(r.Context(), userContextKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// UserFromContext extracts the authenticated user from the request context.
func UserFromContext(ctx context.Context) *database.User {
	u, _ := ctx.Value(userContextKey).(*database.User)
	return u
}

// HashPassword generates a bcrypt hash of the plain-text password.
func HashPassword(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	return string(b), err
}

// CheckPassword verifies a plain-text password against a stored bcrypt hash.
// Returns nil on match, non-nil otherwise.
func CheckPassword(hash, plain string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain))
}

// SeedAdminToUserTableIfNoUserExistsYet creates the default admin user in the application user table if no users exist yet.
func SeedAdminToUserTableIfNoUserExistsYet(db *sql.DB) error {

	ctx := context.Background()

	anyUserExistsYet, err := database.DoesAnyUserExistYet(ctx, db)

	if err != nil {
		return err
	}

	if anyUserExistsYet {
		return nil
	}

	log.Print("No users exist yet, seeding default admin user with username 'admin' and password 'TestPasswordChangeMe.' to the user table. \n" +
		"Please change the password as soon as possible and after first login!")

	hash, err := HashPassword("TestPasswordChangeMe.")

	if err != nil {
		return err
	}

	return database.CreateUser(ctx, db, "admin", hash, true, true)

}
