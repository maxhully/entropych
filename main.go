package main

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"

	"crawshaw.io/sqlite"
	"crawshaw.io/sqlite/sqlitex"
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
type DB struct {
	dbpool *sqlitex.Pool
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

func CreatePost(conn *sqlite.Conn, userID int64, content string) (*Post, error) {
	query := "insert into post (user_id, created_at, content) values (?, ?, ?)"
	err := sqlitex.Exec(conn, query, nil, userID, time.Now().UTC().Unix(), content)
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

type App struct {
	templates *template.Template
	bufpool   *bpool.BufferPool
	db        *DB
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

func NewApp(db *DB) *App {
	return &App{
		templates: template.Must(template.ParseGlob("templates/*")),
		db:        db,
		bufpool:   bpool.NewBufferPool(48),
	}
}

func errorResponse(w http.ResponseWriter, err error) {
	log.Printf("sending 500 error: %s", err)
	http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
}

func (app *App) RenderTemplate(w http.ResponseWriter, name string, data any) {
	// We render to a buffer (from the buffer pool) so that we can handle template
	// execution errors (without sending half a template response first).
	buf := app.bufpool.Get()
	defer app.bufpool.Put(buf)
	err := app.templates.ExecuteTemplate(buf, name, data)
	if err != nil {
		errorResponse(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (app *App) Homepage(w http.ResponseWriter, r *http.Request) {
	conn := app.db.dbpool.Get(r.Context())
	defer app.db.dbpool.Put(conn)

	posts, err := GetRecentPosts(conn, 10)
	if err != nil {
		errorResponse(w, err)
		return
	}
	app.RenderTemplate(w, "index.html", posts)
}

func (app *App) HelloUser(w http.ResponseWriter, r *http.Request) {
	conn := app.db.dbpool.Get(r.Context())
	defer app.db.dbpool.Put(conn)

	q := r.URL.Query()
	name := q.Get("name")
	if name == "" {
		name = "Max"
	}
	user, err := GetUserByName(conn, name)
	if err != nil {
		errorResponse(w, err)
		return
	}
	if user == nil {
		user = &User{
			Name: name,
		}
	}
	app.RenderTemplate(w, "hello.html", user)
}

type SignUpForm struct {
	Name     string
	Password string
	// rename to Problems?
	Errors map[string][]string
}

func (f *SignUpForm) ParseFrom(r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return err
	}
	f.Name = r.PostForm.Get("name")
	f.Password = r.PostForm.Get("password")
	return nil
}

func (f *SignUpForm) PushError(fieldName string, errorMessage string) {
	f.Errors[fieldName] = append(f.Errors[fieldName], errorMessage)
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
	conn := app.db.dbpool.Get(r.Context())
	defer app.db.dbpool.Put(conn)

	form := SignUpForm{
		Errors: make(map[string][]string),
	}

	if r.Method != http.MethodPost {
		app.RenderTemplate(w, "sign_up.html", form)
		return
	}
	if err := form.ParseFrom(r); err != nil {
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

	if _, err := CreateUser(conn, form.Name, form.Password); err != nil {
		errorResponse(w, err)
		return
	}

	url := fmt.Sprintf("/user?name=%s", url.QueryEscape(form.Name))
	http.Redirect(w, r, url, http.StatusSeeOther)
}

type LogInForm = SignUpForm

// Writing errors onto the login form is probably a bad way to do this, in terms of API design.
// But it's a starting point.
func checkLogInForm(conn *sqlite.Conn, form *LogInForm) (canLogIn bool, err error) {
	var hashAndSalt HashAndSalt
	foundUser := false
	query := "select password_salt, password_hash from user where name = ? limit 1"
	collect := func(stmt *sqlite.Stmt) error {
		var err error
		if hashAndSalt.Salt, err = io.ReadAll(stmt.ColumnReader(0)); err != nil {
			return err
		}
		if hashAndSalt.Hash, err = io.ReadAll(stmt.ColumnReader(1)); err != nil {
			return err
		}
		foundUser = true
		return err
	}
	if err := sqlitex.Exec(conn, query, collect, form.Name); err != nil {
		return false, err
	}
	if !foundUser {
		// Some people discourage revealing this information in your login form, for
		// security reasons. But this is a social networking site where the existence of
		// a user with a given username is public knowledge. (Also, even on a private
		// site, the registration form will often give away this info anyways.)
		form.PushError("name", "There is no user with this name")
		return false, nil
	}

	canLogIn = CheckPassword([]byte(form.Password), hashAndSalt)
	if !canLogIn {
		form.PushError("password", "This password is incorrect")
	}

	return canLogIn, nil
}

func (app *App) LogIn(w http.ResponseWriter, r *http.Request) {
	conn := app.db.dbpool.Get(r.Context())
	defer app.db.dbpool.Put(conn)

	form := LogInForm{
		Errors: make(map[string][]string),
	}
	if r.Method != http.MethodPost {
		app.RenderTemplate(w, "log_in.html", form)
		return
	}
	if err := form.ParseFrom(r); err != nil {
		badRequest(w)
		return
	}

	canLogIn, err := checkLogInForm(conn, &form)
	if err != nil {
		errorResponse(w, err)
		return
	}
	if !canLogIn {
		app.RenderTemplate(w, "log_in.html", form)
		return
	}

	// TODO: set session cookie

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func badRequest(w http.ResponseWriter) {
	http.Error(w, "400 Bad Request", http.StatusBadRequest)
}

func (app *App) NewPost(w http.ResponseWriter, r *http.Request) {
	conn := app.db.dbpool.Get(r.Context())
	defer app.db.dbpool.Put(conn)

	// TODO: authentication and stuff
	user, err := GetUserByName(conn, "Max")
	if err != nil {
		errorResponse(w, err)
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

// I might call this distorted.social, since entropy.social is taken
func main() {
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
	mux.HandleFunc("/", app.Homepage)
	mux.HandleFunc("/user", app.HelloUser)
	mux.HandleFunc("GET /signup", app.SignUpUser)
	mux.HandleFunc("POST /signup", app.SignUpUser)
	mux.HandleFunc("GET /login", app.LogIn)
	mux.HandleFunc("POST /login", app.LogIn)
	mux.HandleFunc("POST /posts/new", app.NewPost)
	mux.Handle("/static/", http.StripPrefix("/static", http.FileServer(http.Dir("./static"))))
	http.ListenAndServe(":7777", mux)
}
