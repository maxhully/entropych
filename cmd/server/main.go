// Runs the entropy.social server

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"crawshaw.io/sqlite"
	"crawshaw.io/sqlite/sqlitex"
	"github.com/gorilla/csrf"
	"github.com/maxhully/entropy"
)

// Not sure how I feel about this. Is there a real point to having these Renderer and DB
// structs in here, or should I flatten this out?
type App struct {
	renderer *entropy.Renderer
	db       *entropy.DB
}

func timer(name string) func() {
	start := time.Now()
	return func() {
		duration := time.Since(start)
		log.Printf("%s took %s", name, duration)
	}
}

func NewApp(db *entropy.DB) *App {
	renderer, err := entropy.NewRenderer()
	if err != nil {
		log.Fatalf("error from NewRenderer: %s", err)
	}
	return &App{
		// TODO: how do I make base templates work...?!
		renderer: renderer,
		db:       db,
	}
}

// It occurs to me that if I had this do nothing when err == nil, then I could do
// ```
// defer errorResponse(w, &err)
// ```
// to return 500 on any errors. But that feels like a real invitation to confusion.
func errorResponse(w http.ResponseWriter, err error) {
	log.Printf("sending 500 error: %s", err)
	http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
}

type userCtxKeyType struct{}

var userCtxKey = userCtxKeyType{}

// Adds the requesting user (if they're logged in) to the request context
func (app *App) withUserContextMiddleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn := app.db.GetReadOnly(r.Context())
		defer app.db.PutReadOnly(conn)
		user, err := entropy.GetUserIfLoggedIn(conn, r)
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
func getCurrentUser(ctx context.Context) *entropy.User {
	user, ok := ctx.Value(userCtxKey).(*entropy.User)
	// Being verbose to acknowledge that this wil be nil, and that's OK, if the user
	// isn't logged in
	if !ok {
		return nil
	}
	return user
}

func (app *App) RenderTemplate(w http.ResponseWriter, name string, data any) {
	err := app.renderer.ExecuteTemplate(w, name, data)
	if err != nil {
		errorResponse(w, err)
	}
}

type homepage struct {
	User        *entropy.User
	Posts       []entropy.Post
	NextPageURL string
	CSRFField   template.HTML
}

const timeQueryParamLayout = "20060102T150405"

// A time in the future, so that we don't filter on time at all
func defaultBefore() time.Time {
	return time.Now().UTC().Add(time.Hour)
}

func (app *App) Homepage(w http.ResponseWriter, r *http.Request) {
	conn := app.db.GetReadOnly(r.Context())
	defer app.db.PutReadOnly(conn)
	before := defaultBefore()
	beforeRaw := r.URL.Query().Get("before")
	var err error
	if beforeRaw != "" {
		// Just ignore errors here
		before, err = time.Parse(timeQueryParamLayout, beforeRaw)
		if err != nil {
			before = defaultBefore()
		}
	}
	user := getCurrentUser(r.Context())
	posts, err := entropy.GetRecommendedPosts(conn, user, before, 50)
	if err != nil {
		errorResponse(w, err)
		return
	}
	page := &homepage{
		User:      user,
		Posts:     posts,
		CSRFField: csrf.TemplateField(r),
	}
	if len(posts) > 0 {
		lastPostCreatedAt := posts[len(posts)-1].CreatedAt.UTC().Format(timeQueryParamLayout)
		page.NextPageURL = fmt.Sprintf("/?before=%s", url.QueryEscape(lastPostCreatedAt))
	}
	app.RenderTemplate(w, "index.html", page)
}

func (app *App) About(w http.ResponseWriter, r *http.Request) {
	app.RenderTemplate(w, "about.html", nil)
}

type userPostsPage struct {
	LoggedInUser           *entropy.User
	PostingUser            *entropy.User
	Posts                  []entropy.Post
	IsFollowingPostingUser bool
	PostingUserFollowStats *entropy.UserFollowStats
	DistanceFromUser       int
	CSRFField              template.HTML
}

func getUserPostsPage(conn *sqlite.Conn, user *entropy.User, postingUser *entropy.User) (*userPostsPage, error) {
	isFollowing := false
	distanceFromUser := 0
	var err error
	if user != nil && postingUser.UserID != user.UserID {
		// TODO: I could consolidate these queries
		isFollowing, err = entropy.IsFollowing(conn, user.UserID, postingUser.UserID)
		if err != nil {
			return nil, err
		}
		// Maybe just debug info
		distances, err := entropy.GetDistanceFromUser(conn, user.UserID, []int64{postingUser.UserID})
		if err != nil {
			return nil, err
		}
		distanceFromUser = distances[postingUser.UserID]
	}
	posts, err := entropy.GetRecentPostsFromUser(conn, postingUser.UserID, 50)
	if err != nil {
		return nil, err
	}
	err = entropy.DistortPostsForUser(conn, user, posts)
	if err != nil {
		return nil, err
	}
	stats, err := entropy.GetUserFollowStats(conn, postingUser.UserID)
	if err != nil {
		return nil, err
	}
	return &userPostsPage{
		LoggedInUser:           user,
		PostingUser:            postingUser,
		Posts:                  posts,
		IsFollowingPostingUser: isFollowing,
		PostingUserFollowStats: stats,
		DistanceFromUser:       distanceFromUser,
	}, nil
}

func (app *App) ShowUserPosts(w http.ResponseWriter, r *http.Request) {
	conn := app.db.GetReadOnly(r.Context())
	defer app.db.PutReadOnly(conn)

	postingUserName := r.PathValue("username")
	postingUser, err := entropy.GetUserByName(conn, postingUserName)
	if err != nil {
		errorResponse(w, err)
		return
	}
	if postingUser == nil {
		http.NotFound(w, r)
		return
	}
	user := getCurrentUser(r.Context())
	page, err := getUserPostsPage(conn, user, postingUser)
	if err != nil {
		errorResponse(w, err)
		return
	}
	page.CSRFField = csrf.TemplateField(r)
	app.RenderTemplate(w, "user_posts.html", page)
}

type nameAndPasswordForm struct {
	Name      string
	Password  string
	CSRFField template.HTML
	// rename to Problems?
	Errors map[string]string
}

func (f *nameAndPasswordForm) ParseFromBody(r *http.Request) error {
	// TODO: this should never error, because the CSRF middleware should already have
	// parsed the body. So I should be able to move this into newSignUpForm, etc.
	if err := r.ParseForm(); err != nil {
		return err
	}
	f.Name = r.PostForm.Get("name")
	f.Password = r.PostForm.Get("password")
	return nil
}

type SignUpForm struct {
	nameAndPasswordForm
}

func newSignUpForm(r *http.Request) *SignUpForm {
	return &SignUpForm{nameAndPasswordForm{
		CSRFField: csrf.TemplateField(r),
		Errors:    make(map[string]string),
	}}
}

func (f *SignUpForm) Validate(conn *sqlite.Conn) error {
	// Max length for username and password
	const maxLength = 256

	if len(f.Name) == 0 {
		f.Errors["name"] = "Name is required"
	} else if len(f.Name) > maxLength {
		f.Errors["name"] = fmt.Sprintf("Name is too long (max %d characters)", maxLength)
	} else {
		existingUserWithName, err := entropy.GetUserByName(conn, f.Name)
		if err != nil {
			return err
		}
		if existingUserWithName != nil {
			f.Errors["name"] = "A user with this name already exists"
		}
	}

	if len(f.Password) == 0 {
		f.Errors["password"] = "Password is required"
	} else if len(f.Password) > maxLength {
		f.Errors["password"] = fmt.Sprintf("Password is too long (max %d characters)", maxLength)
	}
	return nil
}

func (app *App) SignUpUser(w http.ResponseWriter, r *http.Request) {
	// TODO: handle when user is already logged in
	conn := app.db.Get(r.Context())
	defer app.db.Put(conn)

	form := newSignUpForm(r)
	if r.Method != http.MethodPost {
		app.RenderTemplate(w, "signup.html", form)
		return
	}
	if err := form.ParseFromBody(r); err != nil {
		badRequest(w, err)
		return
	}
	if err := form.Validate(conn); err != nil {
		errorResponse(w, err)
		return
	}
	if len(form.Errors) > 0 {
		app.RenderTemplate(w, "signup.html", form)
		return
	}

	user, err := entropy.CreateUser(conn, form.Name, form.Password)
	if err != nil {
		errorResponse(w, err)
		return
	}
	session, err := entropy.CreateUserSession(conn, user.UserID)
	if err != nil {
		errorResponse(w, err)
		return
	}
	entropy.SaveSessionInCookie(w, session)

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

type LogInForm struct {
	nameAndPasswordForm
}

// Writing errors onto the login form is probably a bad way to do this, in terms of API design.
// But it's a starting point.
//
// If the returned *User is not nil, then the user can log in.
// If the user exists but the password is wrong, we still return nil.
func checkLogInForm(conn *sqlite.Conn, form *LogInForm) (*entropy.User, error) {
	var hashAndSalt entropy.HashAndSalt
	var user *entropy.User
	// TODO: move to db.go?
	query := "select user_id, user_name, password_salt, password_hash from user where user_name = ? limit 1"
	collect := func(stmt *sqlite.Stmt) error {
		var err error
		user = &entropy.User{
			UserID: stmt.ColumnInt64(0),
			Name:   stmt.ColumnText(1),
			// Omitting DisplayName and Bio because we don't need them here
		}
		if hashAndSalt.Salt, err = io.ReadAll(stmt.ColumnReader(2)); err != nil {
			return err
		}
		if hashAndSalt.Hash, err = io.ReadAll(stmt.ColumnReader(3)); err != nil {
			return err
		}
		return err
	}
	if err := sqlitex.Exec(conn, query, collect, form.Name); err != nil {
		return nil, err
	}
	if user == nil {
		// Some people discourage revealing this information in your login form, for
		// security reasons. But this is a social networking site where the existence of
		// a user with a given username is public knowledge. (Also, even on a private
		// site, the registration form will often give away this info anyways.)
		form.Errors["name"] = "There is no user with this name"
		return nil, nil
	}
	if !entropy.CheckPassword([]byte(form.Password), hashAndSalt) {
		form.Errors["password"] = "This password is incorrect"
		return nil, nil
	}
	// The user can log in!
	return user, nil
}

func newLogInForm(r *http.Request) LogInForm {
	return LogInForm{nameAndPasswordForm{
		CSRFField: csrf.TemplateField(r),
		Errors:    make(map[string]string),
	}}
}

func (app *App) LogIn(w http.ResponseWriter, r *http.Request) {
	conn := app.db.Get(r.Context())
	defer app.db.Put(conn)

	form := newLogInForm(r)
	if r.Method != http.MethodPost {
		app.RenderTemplate(w, "login.html", form)
		return
	}
	if err := form.ParseFromBody(r); err != nil {
		badRequest(w, err)
		return
	}

	user, err := checkLogInForm(conn, &form)
	if err != nil {
		errorResponse(w, err)
		return
	}
	if user == nil {
		app.RenderTemplate(w, "login.html", form)
		return
	}

	session, err := entropy.CreateUserSession(conn, user.UserID)
	if err != nil {
		errorResponse(w, err)
		return
	}
	entropy.SaveSessionInCookie(w, session)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func badRequest(w http.ResponseWriter, err error) {
	log.Printf("sending 400 error: %s", err)
	http.Error(w, "400 Bad Request", http.StatusBadRequest)
}

func redirectToLogin(w http.ResponseWriter, r *http.Request) {
	// Clear the session cookie in case it has expired
	entropy.ClearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (app *App) LogOut(w http.ResponseWriter, r *http.Request) {
	entropy.ClearSessionCookie(w)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (app *App) NewPost(w http.ResponseWriter, r *http.Request) {
	conn := app.db.Get(r.Context())
	defer app.db.Put(conn)

	user := getCurrentUser(r.Context())
	if user == nil {
		redirectToLogin(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		badRequest(w, err)
		return
	}
	content := r.PostForm.Get("content")
	// should empty posts be allowed?
	_, err := entropy.CreatePost(conn, user.UserID, content)
	if err != nil {
		errorResponse(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (app *App) FollowUser(w http.ResponseWriter, r *http.Request) {
	conn := app.db.Get(r.Context())
	defer app.db.Put(conn)

	user := getCurrentUser(r.Context())
	if user == nil {
		redirectToLogin(w, r)
		return
	}

	username := r.PathValue("username")
	followedUser, err := entropy.GetUserByName(conn, username)
	if err != nil {
		errorResponse(w, err)
		return
	}
	if followedUser == nil || user.UserID == followedUser.UserID {
		http.NotFound(w, r)
		return
	}

	err = entropy.FollowUser(conn, user.UserID, followedUser.UserID)
	if err != nil {
		errorResponse(w, err)
		return
	}

	http.Redirect(w, r, followedUser.URL(), http.StatusSeeOther)
}

// lol, a lot of duplication here
func (app *App) UnfollowUser(w http.ResponseWriter, r *http.Request) {
	conn := app.db.Get(r.Context())
	defer app.db.Put(conn)

	user := getCurrentUser(r.Context())
	if user == nil {
		redirectToLogin(w, r)
		return
	}

	username := r.PathValue("username")
	followedUser, err := entropy.GetUserByName(conn, username)
	if err != nil {
		errorResponse(w, err)
		return
	}
	if followedUser == nil || user.UserID == followedUser.UserID {
		http.NotFound(w, r)
		return
	}

	err = entropy.UnfollowUser(conn, user.UserID, followedUser.UserID)
	if err != nil {
		errorResponse(w, err)
		return
	}

	http.Redirect(w, r, followedUser.URL(), http.StatusSeeOther)
}

// TODO: reset password and stuff
type updateProfileForm struct {
	DisplayName string
	Bio         string
	Errors      map[string]string
}

type updateProfilePage struct {
	Form      updateProfileForm
	User      *entropy.User
	CSRFField template.HTML
}

func (f *updateProfileForm) Validate() {
	if len(f.DisplayName) > 256 {
		f.Errors["display_name"] = fmt.Sprintf("Display name is too long (max %d characters)", 256)
	}
	if len(f.Bio) > 256 {
		f.Errors["bio"] = fmt.Sprintf("Bio is too long (max %d characters)", 256)
	}
}

func (app *App) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	conn := app.db.Get(r.Context())
	defer app.db.Put(conn)
	user := getCurrentUser(r.Context())
	if user == nil {
		redirectToLogin(w, r)
		return
	}
	// TODO: parse body with 1MB upload limit
	if err := r.ParseForm(); err != nil {
		badRequest(w, err)
		return
	}
	var page updateProfilePage
	page.User = user
	page.Form.Errors = make(map[string]string)
	page.Form.DisplayName = user.DisplayName
	page.Form.Bio = user.Bio
	page.CSRFField = csrf.TemplateField(r)
	if v := r.PostForm.Get("display_name"); v != "" {
		page.Form.DisplayName = v
	}
	if v := r.PostForm.Get("bio"); v != "" {
		page.Form.Bio = v
	}
	if r.Method == http.MethodGet {
		app.RenderTemplate(w, "user_profile.html", page)
		return
	}
	// TODO: validate avatar upload (must be .png)
	if page.Form.Validate(); len(page.Form.Errors) > 0 {
		app.RenderTemplate(w, "user_profile.html", page)
		return
	}
	var uploadID int64
	file, header, err := r.FormFile("avatar")
	if errors.Is(err, http.ErrMissingFile) {
		uploadID = 0
	} else if err != nil {
		badRequest(w, err)
		return
	} else {
		if uploadID, err = saveUpload(conn, file, header); err != nil {
			errorResponse(w, err)
			return
		}
	}
	err = entropy.UpdateUserProfile(conn, user.Name, page.Form.DisplayName, page.Form.Bio, uploadID)
	if err != nil {
		errorResponse(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func saveUpload(conn *sqlite.Conn, file multipart.File, header *multipart.FileHeader) (int64, error) {
	contents, err := io.ReadAll(file)
	if err != nil {
		return 0, err
	}
	contentTypes := header.Header["Content-Type"]
	if len(contentTypes) != 1 {
		// TODO: make this a 400?
		return 0, fmt.Errorf("unexpected mime header (zero or >1 content types?): %+v", header)
	}
	contentType := contentTypes[0]
	stem, err := randomHex()
	if err != nil {
		return 0, err
	}
	exts, err := mime.ExtensionsByType(contentType)
	if err != nil {
		return 0, err
	}
	filename := stem + exts[0]
	return entropy.SaveUpload(conn, filename, contentType, contents)
}

func randomHex() (string, error) {
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		// Should probably just panic, tbh
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func (app *App) ServeUpload(w http.ResponseWriter, r *http.Request) {
	conn := app.db.GetReadOnly(r.Context())
	defer app.db.PutReadOnly(conn)
	parts := strings.SplitN(r.PathValue("upload_id"), ".", 2)
	if len(parts) != 2 || strings.ToLower(parts[1]) != "png" {
		http.NotFound(w, r)
		return
	}
	uploadID, err := strconv.Atoi(parts[0])
	if err != nil {
		http.NotFound(w, r)
		return
	}
	blob, contentType, err := entropy.OpenUploadContents(conn, int64(uploadID))
	if err != nil {
		errorResponse(w, err)
		return
	}
	defer blob.Close()
	w.Header().Set("Content-Type", contentType)
	// TODO: should probably buffer this and handle errors?
	io.Copy(w, blob)
}

func main() {
	t := timer("startup")
	// Might want this to be in a secret file instead
	secretKeyFlag := flag.String("secret-key", "", "Secret key for signed cookies (hex encoded)")
	flag.Parse()

	if secretKeyFlag == nil || len(*secretKeyFlag) == 0 {
		log.Fatal("--secret-key is required")
	}
	secretKeyHex := *secretKeyFlag
	secretKey, err := hex.DecodeString(secretKeyHex)
	if err != nil {
		log.Fatal("--secret-key must be hex-encoded")
	}
	if len(secretKey) != 32 {
		log.Fatal("--secret-key must be 32 bytes")
	}

	db, err := entropy.NewDB("test.db", 10)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	app := NewApp(db)

	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static", http.FileServer(http.Dir("./static"))))

	// TODO: Maybe I should wrap these handlers somehow so that they can just return an
	// error, instead of calling errorResponse for every possible 500
	mux.HandleFunc("GET /{$}", app.Homepage)
	mux.HandleFunc("GET /about", app.About)

	mux.HandleFunc("GET /signup", app.SignUpUser)
	mux.HandleFunc("POST /signup", app.SignUpUser)
	mux.HandleFunc("GET /login", app.LogIn)
	mux.HandleFunc("POST /login", app.LogIn)
	mux.HandleFunc("POST /logout", app.LogOut)
	mux.HandleFunc("GET /profile", app.UpdateProfile)
	mux.HandleFunc("POST /profile", app.UpdateProfile)

	mux.HandleFunc("POST /posts/new", app.NewPost)

	mux.HandleFunc("/u/{username}/{$}", app.ShowUserPosts)
	mux.HandleFunc("POST /u/{username}/follow", app.FollowUser)
	mux.HandleFunc("POST /u/{username}/unfollow", app.UnfollowUser)

	mux.HandleFunc("GET /uploads/{upload_id}", app.ServeUpload)

	csrfProtect := csrf.Protect(secretKey, csrf.FieldName("csrf_token"))
	server := csrfProtect(app.withUserContextMiddleware(mux))
	t()

	http.ListenAndServe(":7777", server)
}
