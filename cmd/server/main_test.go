package main

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"strings"
	"testing"

	"github.com/maxhully/entropy"
	"github.com/stretchr/testify/assert"
)

func setUpTestApp(t *testing.T) (*App, error) {
	dir := t.TempDir()
	uri := path.Join(dir, "temptest.db")
	db, err := entropy.NewDB(uri, 10)
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
		t.Errorf("expected %#v in the body:\n%s", substr, body)
	}
}

func TestHomepage(t *testing.T) {
	app, err := setUpTestApp(t)
	if err != nil {
		t.Fatal(err)
	}
	defer app.db.Close()

	r, _ := http.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	app.Homepage(w, r)

	result := w.Result()
	if result.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", result.StatusCode)
	}
	checkBodyContains(t, result, "Hello, stranger!")
}

func TestSignUpUser(t *testing.T) {
	app, err := setUpTestApp(t)
	if err != nil {
		t.Fatal(err)
	}
	defer app.db.Close()

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

	conn := app.db.Get(t.Context())
	defer app.db.Put(conn)
	user, err := entropy.GetUserByName(conn, "Max")
	assert.Nil(t, err)
	assert.Equal(t, user.Name, "Max")
	assert.NotEqual(t, user.AvatarUploadID, 0, "Expected default avatar to be generated")
}

func TestSignUpUserValidation(t *testing.T) {
	var testCases = []struct {
		name                 string
		expectedErrorMessage string
	}{
		{"", "Name is required"},
		{"max hully", "Name must not have any spaces in it"},
		{"max", "A user with this name already exists"},
	}

	for _, testCase := range testCases {
		t.Run(fmt.Sprintf("%q", testCase.name), func(t *testing.T) {
			app, err := setUpTestApp(t)
			if err != nil {
				t.Fatal(err)
			}
			defer app.db.Close()

			// So that the user already exists
			conn := app.db.Get(t.Context())
			entropy.CreateUser(conn, "max", "secretpassword123")
			app.db.Put(conn)

			form := url.Values{}
			form.Add("name", testCase.name)
			form.Add("password", "secretpassword456")

			r, _ := http.NewRequest(http.MethodPost, "/signup", strings.NewReader(form.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			app.SignUpUser(w, r)

			result := w.Result()
			assert.Equal(t, http.StatusOK, result.StatusCode)
			checkBodyContains(t, result, testCase.expectedErrorMessage)
		})
	}
}

func TestLogInUser(t *testing.T) {
	var testCases = []struct {
		name                 string
		password             string
		expectedStatus       int
		expectedErrorMessage string
	}{
		{"max", "wrongpassword", 200, "password is incorrect"},
		{"notmax", "secretpassword123", 200, "no user with this name"},
		{"max", "secretpassword123", 303, ""},
	}

	for _, testCase := range testCases {
		t.Run(fmt.Sprintf("%v", testCase), func(t *testing.T) {
			app, err := setUpTestApp(t)
			if err != nil {
				t.Fatal(err)
			}
			defer app.db.Close()

			{
				conn := app.db.Get(t.Context())
				entropy.CreateUser(conn, "max", "secretpassword123")
				app.db.Put(conn)
			}

			form := url.Values{}
			form.Add("name", testCase.name)
			form.Add("password", testCase.password)

			r, _ := http.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()

			app.LogIn(w, r)

			result := w.Result()
			if result.StatusCode != testCase.expectedStatus {
				t.Fatalf("expected %d, got %d", testCase.expectedStatus, result.StatusCode)
			}
			if len(testCase.expectedErrorMessage) > 0 {
				checkBodyContains(t, result, testCase.expectedErrorMessage)
			} else {
				cookies := w.Result().Cookies()
				if len(cookies) != 1 {
					t.Errorf("expected 1 cookie set; got %d", len(cookies))
				}
				if cookies[0].Name != "id" {
					t.Errorf("expected cookie named %#v; got %#v", "id", cookies[0].Name)
				}
			}
		})
	}
}

func TestUpdateProfile(t *testing.T) {
	app, err := setUpTestApp(t)
	assert.Nil(t, err)
	defer app.db.Close()

	var sess *entropy.UserSession
	var originalAvatarUploadID int64
	{
		conn := app.db.Get(t.Context())
		user, err := entropy.CreateUser(conn, "max", "pass123")
		originalAvatarUploadID = user.AvatarUploadID
		assert.Nil(t, err)
		sess, err = entropy.CreateUserSession(conn, user.UserID)
		app.db.Put(conn)
	}
	assert.NotEqual(t, originalAvatarUploadID, 0)

	body := new(bytes.Buffer)
	mw := multipart.NewWriter(body)
	mw.WriteField("bio", "Hello!")
	mw.Close()

	h := entropy.WithUserContextMiddleware(app.db, http.HandlerFunc(app.UpdateProfile))

	// First do a GET
	r, _ := http.NewRequest(http.MethodGet, "/profile", nil)
	r.AddCookie(sess.ToCookie())
	w := httptest.NewRecorder()

	h.ServeHTTP(w, r)
	assert.Equal(t, w.Result().StatusCode, http.StatusOK)

	// Then POST
	r, _ = http.NewRequest(http.MethodPost, "/profile", body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.AddCookie(sess.ToCookie())
	w = httptest.NewRecorder()

	h.ServeHTTP(w, r)
	assert.Equal(t, w.Result().StatusCode, http.StatusSeeOther)

	conn := app.db.Get(t.Context())
	defer app.db.Put(conn)
	user, err := entropy.GetUserByName(conn, "max")
	assert.Nil(t, err)
	assert.Equal(t, user.Bio, "Hello!")
	// We shouldn't change the avatar, since we didn't upload anything
	assert.Equal(t, user.AvatarUploadID, originalAvatarUploadID)
}

// TODO: test with upload
