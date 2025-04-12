package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"crawshaw.io/sqlite/sqlitex"
)

func setUpTestApp() (*App, error) {
	dbpool, err := sqlitex.Open("file::memory:?mode=memory", 0, 1)
	if err != nil {
		return nil, err
	}
	db, err := NewDB(dbpool)
	if err != nil {
		return nil, err
	}
	app := NewApp(db)
	return app, err
}

func checkBodyContains(t *testing.T, resp *http.Response, substr string) {
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	body := string(bodyBytes)

	if !strings.Contains(body, substr) {
		t.Errorf("expected %v in the body:\n%s", substr, body)
	}
}

func TestHomepage(t *testing.T) {
	app, err := setUpTestApp()
	if err != nil {
		t.Fatal(err)
	}

	r, _ := http.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	app.Homepage(w, r)

	result := w.Result()
	if result.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", result.StatusCode)
	}
	checkBodyContains(t, result, "Hello!")
}

func TestSignUpUser(t *testing.T) {
	app, err := setUpTestApp()
	if err != nil {
		t.Fatal(err)
	}

	r, _ := http.NewRequest(http.MethodGet, "/signup", nil)
	w := httptest.NewRecorder()
	app.SignUpUser(w, r)

	result := w.Result()
	if result.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", result.StatusCode)
	}

	form := url.Values{}
	form.Add("name", "Max")
	form.Add("password", "secretpassword123")

	r, _ = http.NewRequest(http.MethodPost, "/signup", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	app.SignUpUser(w, r)

	result = w.Result()
	if result.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 See Other, got %d", result.StatusCode)
	}

	user, err := app.db.GetUserByName(context.TODO(), "Max")
	if err != nil {
		t.Error(err)
	}
	if user.Name != "Max" {
		t.Errorf("expected Max, got %s", user.Name)
	}
}
