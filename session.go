package entropy

import (
	"encoding/hex"
	"fmt"
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
		SameSite: http.SameSiteStrictMode,
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
