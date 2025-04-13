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
	mathrand "math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"crawshaw.io/sqlite"
	"crawshaw.io/sqlite/sqlitex"
	"github.com/gorilla/csrf"
	"github.com/oxtoacart/bpool"
)

type User struct {
	UserID int64
	Name   string
}

func (u *User) Exists() bool {
	return u.UserID > 0
}

// It seems like a lot of go code ties the connection to the context (and passes around
// the context), rather than passing around the actual connection. But I'm happy with
// passing around the connection (for now, at least).
//
// I'm curious if there's a way that I can make the db error handling nicer. We don't
// want to panic, but we also don't expect them to happen almost ever.
//
// ...Maybe I _do_ want to panic on unexpected SQLite errors?
type DB struct {
	dbpool *sqlitex.Pool
}

func randomContentRune() rune {
	// TODO: find more fun content ranges
	// This is the "Basic Latin" range of code points
	minRune := 0x0020
	maxRune := 0x007F
	i := mathrand.Intn(maxRune - minRune)
	return rune(minRune + i)
}

const maxGraphDistance = 6.0

func DistortContent(content string, graphDistance int) string {
	if graphDistance == 0 {
		return content
	}
	var builder strings.Builder
	builder.Grow(len(content))

	// graphDistance is between 1 and 6, say.
	p := min(float32(graphDistance)/maxGraphDistance, 1.0)

	// TODO: wrap the errors in <mark> tags in a different style
	for _, r := range content {
		if mathrand.Float32() > p {
			builder.WriteRune(r)
		} else {
			builder.WriteRune(randomContentRune())
		}
	}
	return builder.String()
}

func GetRecentPosts(conn *sqlite.Conn, limit int) ([]Post, error) {
	var posts []Post
	query := `
		select post_id, user.name, created_at, content
		from post
		join user using (user_id)
		order by created_at desc
		limit ?`
	collect := func(stmt *sqlite.Stmt) error {
		post := Post{
			PostID:    stmt.ColumnInt64(0),
			UserName:  stmt.ColumnText(1),
			CreatedAt: time.Unix(stmt.ColumnInt64(2), 0),
			Content:   stmt.ColumnText(3),
		}
		posts = append(posts, post)
		return nil
	}
	err := sqlitex.Exec(conn, query, collect, limit)
	return posts, err
}

func GetRecentPostsFromUser(conn *sqlite.Conn, userID int64, limit int) ([]Post, error) {
	var posts []Post
	query := `
		select post_id, user.name, created_at, content
		from post
		join user using (user_id)
		where user_id = ?
		order by created_at desc
		limit ?`
	collect := func(stmt *sqlite.Stmt) error {
		post := Post{
			PostID:    stmt.ColumnInt64(0),
			UserName:  stmt.ColumnText(1),
			CreatedAt: time.Unix(stmt.ColumnInt64(2), 0),
			Content:   stmt.ColumnText(3),
		}
		posts = append(posts, post)
		return nil
	}
	err := sqlitex.Exec(conn, query, collect, userID, limit)
	return posts, err
}

func GetUserByName(conn *sqlite.Conn, name string) (*User, error) {
	var user *User = nil
	query := "select user_id, name from user where name = ? limit 1"
	collect := func(stmt *sqlite.Stmt) error {
		user = &User{
			UserID: stmt.ColumnInt64(0),
			Name:   stmt.ColumnText(1),
		}
		return nil
	}
	err := sqlitex.Exec(conn, query, collect, name)
	return user, err
}

type Post struct {
	PostID    int64
	UserName  string
	CreatedAt time.Time
	Content   string
}

func (p *Post) UserURL() string {
	return fmt.Sprintf("/u/%s/", url.PathEscape(p.UserName))
}

func utcNow() time.Time {
	return time.Now().UTC()
}

func CreatePost(conn *sqlite.Conn, userID int64, content string) (*Post, error) {
	query := "insert into post (user_id, created_at, content) values (?, ?, ?)"
	err := sqlitex.Exec(conn, query, nil, userID, utcNow().Unix(), content)
	if err != nil {
		return nil, err
	}
	postID := conn.LastInsertRowID()

	var post *Post
	query = `
		select post_id, user.name, created_at, content
		from post
		join user using (user_id)
		where post_id = ?`

	collect := func(stmt *sqlite.Stmt) error {
		post = &Post{
			PostID:    stmt.ColumnInt64(0),
			UserName:  stmt.ColumnText(1),
			CreatedAt: time.Unix(stmt.ColumnInt64(2), 0),
			Content:   stmt.ColumnText(3),
		}
		return nil
	}
	err = sqlitex.Exec(conn, query, collect, postID)
	return post, err
}

func CreateUser(conn *sqlite.Conn, name string, password string) (*User, error) {
	hashAndSalt, err := HashAndSaltPassword([]byte(password))
	if err != nil {
		return nil, err
	}

	query := "insert into user (name, password_salt, password_hash) values (?, ?, ?)"
	err = sqlitex.Exec(conn, query, nil, name, hashAndSalt.Salt, hashAndSalt.Hash)
	if err != nil {
		return nil, err
	}

	userID := conn.LastInsertRowID()
	user := &User{UserID: userID, Name: name}
	return user, err
}

// Not sure how I feel about this. Is there a real point to having these Renderer and DB
// structs in here, or should I flatten this out?
type App struct {
	renderer *Renderer
	db       *DB
}

func setUpDb(conn *sqlite.Conn) error {
	sqlBytes, err := os.ReadFile("schema.sql")
	if err != nil {
		return err
	}
	sql := string(sqlBytes)
	return sqlitex.ExecScript(conn, sql)
}

func NewDB(dbpool *sqlitex.Pool) (*DB, error) {
	conn := dbpool.Get(context.TODO())
	if conn == nil {
		return nil, errors.New("couldn't get a connection")
	}
	defer dbpool.Put(conn)

	err := setUpDb(conn)
	if err != nil {
		return nil, fmt.Errorf("couldn't set up db: %s", err)
	}

	return &DB{dbpool: dbpool}, nil
}

// This is a little abstraction around template.Template to make a "base" layout
// template work.
//
// This is inspired mainly by staring at the pkgsite source code:
// https://github.com/golang/pkgsite/blob/master/internal/frontend/templates/templates.go
type Renderer struct {
	templates        map[string]*template.Template
	baseTemplateName string
	bufpool          *bpool.BufferPool
}

func (r *Renderer) ExecuteTemplate(w io.Writer, name string, data any) error {
	t := r.templates[name]
	return t.ExecuteTemplate(w, r.baseTemplateName, data)
}

func NewRenderer(baseTemplatePath string, glob string) (*Renderer, error) {
	renderer := Renderer{
		templates:        make(map[string]*template.Template),
		baseTemplateName: filepath.Base(baseTemplatePath),
		bufpool:          bpool.NewBufferPool(48),
	}
	paths, err := filepath.Glob(glob)
	if err != nil {
		return nil, err
	}
	// An awful lot of template.Must going on here...
	// That's probably fine. Crashing on startup is kinda what we want anyway.
	baseTemplate := template.Must(template.ParseFiles(baseTemplatePath))
	for _, path := range paths {
		name := filepath.Base(path)
		t := template.Must(baseTemplate.Clone())
		t = template.Must(t.ParseFiles(baseTemplatePath, path))
		renderer.templates[name] = t
	}
	return &renderer, nil
}

func NewApp(db *DB) *App {
	renderer, err := NewRenderer("templates/base.html", "templates/*.html")
	if err != nil {
		log.Fatalf("error from NewRenderer: %s", err)
	}
	return &App{
		// TODO: how do I make base templates work...?!
		renderer: renderer,
		db:       db,
	}
}

func errorResponse(w http.ResponseWriter, err error) {
	log.Printf("sending 500 error: %s", err)
	http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
}

func (app *App) RenderTemplate(w http.ResponseWriter, name string, data any) {
	// We render to a buffer (from the buffer pool) so that we can handle template
	// execution errors (without sending half a template response first). This sounds
	// reasonable to me, but I get the sense that we have all cargo culted this solution
	// from various blog posts.
	buf := app.renderer.bufpool.Get()
	defer app.renderer.bufpool.Put(buf)
	err := app.renderer.ExecuteTemplate(buf, name, data)
	if err != nil {
		errorResponse(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

type homepage struct {
	User      *User
	Posts     []Post
	CSRFField template.HTML
}

func (app *App) Homepage(w http.ResponseWriter, r *http.Request) {
	conn := app.db.dbpool.Get(r.Context())
	defer app.db.dbpool.Put(conn)

	user, err := getUserIfLoggedIn(conn, r)
	if err != nil {
		errorResponse(w, err)
		return
	}
	posts, err := GetRecentPosts(conn, 10)
	for i := range posts {
		posts[i].Content = DistortContent(posts[i].Content, 1)
	}
	if err != nil {
		errorResponse(w, err)
		return
	}
	app.RenderTemplate(w, "index.html", homepage{User: user, Posts: posts, CSRFField: csrf.TemplateField(r)})
}

type userPostsPage struct {
	LoggedInUser *User
	PostingUser  *User
	Posts        []Post
}

func (app *App) ShowUserPosts(w http.ResponseWriter, r *http.Request) {
	conn := app.db.dbpool.Get(r.Context())
	defer app.db.dbpool.Put(conn)

	postingUserName := r.PathValue("username")
	postingUser, err := GetUserByName(conn, postingUserName)
	if err != nil {
		errorResponse(w, err)
		return
	}
	if postingUser == nil {
		http.NotFound(w, r)
		return
	}

	// TODO: do we need this?
	user, err := getUserIfLoggedIn(conn, r)
	if err != nil {
		errorResponse(w, err)
		return
	}

	posts, err := GetRecentPostsFromUser(conn, postingUser.UserID, 10)
	for i := range posts {
		// TODO: figure out distance from user
		posts[i].Content = DistortContent(posts[i].Content, 1)
	}
	if err != nil {
		errorResponse(w, err)
		return
	}
	app.RenderTemplate(w, "user_posts.html", userPostsPage{LoggedInUser: user, PostingUser: postingUser, Posts: posts})
}

type nameAndPasswordForm struct {
	Name      string
	Password  string
	CSRFField template.HTML
	// rename to Problems?
	Errors map[string][]string
}

type SignUpForm struct {
	nameAndPasswordForm
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

func (f *nameAndPasswordForm) PushError(fieldName string, errorMessage string) {
	f.Errors[fieldName] = append(f.Errors[fieldName], errorMessage)
}

func newSignUpForm(r *http.Request) SignUpForm {
	return SignUpForm{nameAndPasswordForm{
		CSRFField: csrf.TemplateField(r),
		Errors:    make(map[string][]string),
	}}
}

func (f *SignUpForm) Validate(conn *sqlite.Conn) error {
	// Max length for username and password
	const maxLength = 256

	if len(f.Name) == 0 {
		f.PushError("name", "Name is required")
	} else if len(f.Name) > maxLength {
		f.PushError("name", fmt.Sprintf("Name is too long (max %d characters)", maxLength))
	} else {
		existingUserWithName, err := GetUserByName(conn, f.Name)
		if err != nil {
			return err
		}
		if existingUserWithName != nil {
			f.PushError("name", "A user with this name already exists")
		}
	}

	if len(f.Password) == 0 {
		f.PushError("password", "Password is required")
	} else if len(f.Password) > maxLength {
		f.PushError("password", fmt.Sprintf("Password is too long (max %d characters)", maxLength))
	}
	return nil
}

func (app *App) SignUpUser(w http.ResponseWriter, r *http.Request) {
	// TODO: handle when user is already logged in
	conn := app.db.dbpool.Get(r.Context())
	defer app.db.dbpool.Put(conn)

	form := newSignUpForm(r)
	if r.Method != http.MethodPost {
		app.RenderTemplate(w, "sign_up.html", form)
		return
	}
	if err := form.ParseFromBody(r); err != nil {
		badRequest(w)
		return
	}
	if err := form.Validate(conn); err != nil {
		errorResponse(w, err)
		return
	}
	if len(form.Errors) > 0 {
		app.RenderTemplate(w, "sign_up.html", form)
		return
	}

	user, err := CreateUser(conn, form.Name, form.Password)
	if err != nil {
		errorResponse(w, err)
		return
	}
	session, err := CreateUserSession(conn, user.UserID)
	if err != nil {
		errorResponse(w, err)
		return
	}
	SaveSessionInCookie(w, session)

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
func checkLogInForm(conn *sqlite.Conn, form *LogInForm) (*User, error) {
	var hashAndSalt HashAndSalt
	var user *User
	query := "select user_id, name, password_salt, password_hash from user where name = ? limit 1"
	collect := func(stmt *sqlite.Stmt) error {
		var err error
		user = &User{
			UserID: stmt.ColumnInt64(0),
			Name:   stmt.ColumnText(1),
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
		form.PushError("name", "There is no user with this name")
		return nil, nil
	}
	if !CheckPassword([]byte(form.Password), hashAndSalt) {
		form.PushError("password", "This password is incorrect")
		return nil, nil
	}
	// The user can log in!
	return user, nil
}

func newLogInForm(r *http.Request) LogInForm {
	return LogInForm{nameAndPasswordForm{
		CSRFField: csrf.TemplateField(r),
		Errors:    make(map[string][]string),
	}}
}

type UserSession struct {
	UserID          int64
	SessionPublicID []byte
	ExpirationTime  time.Time
}

const defaultSessionDuration time.Duration = time.Minute * 30

func CreateUserSession(conn *sqlite.Conn, userID int64) (*UserSession, error) {
	sessionPublicID := make([]byte, 128)
	if _, err := rand.Read(sessionPublicID); err != nil {
		return nil, err
	}
	now := utcNow()
	expirationTime := now.Add(defaultSessionDuration)
	query := `
		insert into user_session (user_id, session_public_id, created_at, expiration_time)
		values (?, ?, ?, ?)`
	err := sqlitex.Exec(conn, query, nil, userID, sessionPublicID, now.Unix(), expirationTime.Unix())
	if err != nil {
		return nil, err
	}
	session := &UserSession{
		UserID:          userID,
		SessionPublicID: sessionPublicID,
		ExpirationTime:  expirationTime,
	}
	return session, err
}

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

func (app *App) LogIn(w http.ResponseWriter, r *http.Request) {
	conn := app.db.dbpool.Get(r.Context())
	defer app.db.dbpool.Put(conn)

	form := newLogInForm(r)
	if r.Method != http.MethodPost {
		app.RenderTemplate(w, "log_in.html", form)
		return
	}
	if err := form.ParseFromBody(r); err != nil {
		badRequest(w)
		return
	}

	user, err := checkLogInForm(conn, &form)
	if err != nil {
		errorResponse(w, err)
		return
	}
	if user == nil {
		app.RenderTemplate(w, "log_in.html", form)
		return
	}

	session, err := CreateUserSession(conn, user.UserID)
	if err != nil {
		errorResponse(w, err)
		return
	}
	SaveSessionInCookie(w, session)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func badRequest(w http.ResponseWriter) {
	http.Error(w, "400 Bad Request", http.StatusBadRequest)
}

func (app *App) NewPost(w http.ResponseWriter, r *http.Request) {
	conn := app.db.dbpool.Get(r.Context())
	defer app.db.dbpool.Put(conn)

	user, err := getUserIfLoggedIn(conn, r)
	if err != nil {
		errorResponse(w, err)
		return
	}
	if user == nil {
		redirectToLogin(w, r)
		return
	}
	if err = r.ParseForm(); err != nil {
		badRequest(w)
		return
	}
	content := r.PostForm.Get("content")
	// should empty posts be allowed?
	_, err = CreatePost(conn, user.UserID, content)
	if err != nil {
		errorResponse(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func redirectToLogin(w http.ResponseWriter, r *http.Request) {
	// Clear the session cookie in case it has expired
	ClearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func GetUserFromSessionPublicID(conn *sqlite.Conn, sessionPublicID []byte) (*User, error) {
	query := `
		select user_id, user.name
		from user_session
		join user using (user_id)
		where session_public_id = ? and expiration_time > ?`
	var user *User
	collect := func(stmt *sqlite.Stmt) error {
		user = &User{
			UserID: stmt.ColumnInt64(0),
			Name:   stmt.ColumnText(1),
		}
		return nil
	}
	err := sqlitex.Exec(conn, query, collect, sessionPublicID, utcNow().Unix())
	return user, err

}

func getUserIfLoggedIn(conn *sqlite.Conn, r *http.Request) (*User, error) {
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

func (app *App) LogOut(w http.ResponseWriter, r *http.Request) {
	ClearSessionCookie(w)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func main() {
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

	dbpool, err := sqlitex.Open("test.db", 0, 10)
	if err != nil {
		log.Fatal(err)
	}
	defer dbpool.Close()
	db, err := NewDB(dbpool)
	if err != nil {
		log.Fatal(err)
	}
	app := NewApp(db)

	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static", http.FileServer(http.Dir("./static"))))

	mux.HandleFunc("/{$}", app.Homepage)

	mux.HandleFunc("GET /signup", app.SignUpUser)
	mux.HandleFunc("POST /signup", app.SignUpUser)
	mux.HandleFunc("GET /login", app.LogIn)
	mux.HandleFunc("POST /login", app.LogIn)
	mux.HandleFunc("POST /logout", app.LogOut)

	mux.HandleFunc("POST /posts/new", app.NewPost)

	mux.HandleFunc("/u/{username}/{$}", app.ShowUserPosts)

	csrfProtect := csrf.Protect(secretKey, csrf.FieldName("csrf_token"))

	http.ListenAndServe(":7777", csrfProtect(mux))
}
