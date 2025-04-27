package entropy

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"

	"crawshaw.io/sqlite"
)

const sessionIdCookieName = "id"

func SaveSessionInCookie(w http.ResponseWriter, session *UserSession) {
	cookie := http.Cookie{
		Name:     sessionIdCookieName,
		Value:    hex.EncodeToString(session.SessionPublicID),
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
	http.SetCookie(w, &cookie)
}

func ClearSessionCookie(w http.ResponseWriter) {
	cookie := http.Cookie{
		Name:     sessionIdCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	}
	http.SetCookie(w, &cookie)
}

func GetUserIfLoggedIn(conn *sqlite.Conn, r *http.Request) (*User, error) {
	cookies := r.CookiesNamed(sessionIdCookieName)
	if len(cookies) == 0 {
		return nil, nil
	}
	if len(cookies) > 1 {
		return nil, fmt.Errorf("expected 1 cookie with name %#v, got %d", sessionIdCookieName, len(cookies))
	}
	sessionPublicID, err := hex.DecodeString(cookies[0].Value)
	if err != nil {
		return nil, err
	}
	// TODO: handle extending the session
	return GetUserFromSessionPublicID(conn, sessionPublicID)
}

// Copied and pasted from cmd/server/main.go
func errorResponse(w http.ResponseWriter, err error) {
	log.Printf("sending 500 error: %s", err)
	http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
}

type userCtxKeyType struct{}

var userCtxKey = userCtxKeyType{}

// Adds the requesting user (if they're logged in) to the request context
func WithUserContextMiddleware(db *DB, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn := db.GetReadOnly(r.Context())
		defer db.PutReadOnly(conn)
		user, err := GetUserIfLoggedIn(conn, r)
		if err != nil {
			errorResponse(w, err)
			return
		}
		ctx := r.Context()
		ctx = context.WithValue(ctx, userCtxKey, user)
		r = r.WithContext(ctx)
		h.ServeHTTP(w, r)
	})
}

// Get the requesting user (stashed on the Context by withUserContextMiddleware)
func GetCurrentUser(ctx context.Context) *User {
	user, ok := ctx.Value(userCtxKey).(*User)
	// Being verbose to acknowledge that this wil be nil, and that's OK, if the user
	// isn't logged in
	if !ok {
		return nil
	}
	return user
}
