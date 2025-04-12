package main

import (
	"context"
	"errors"
	"fmt"
	"html/template"
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

// TODO: maybe I should refactor this so that all these methods take a connection,
// instead of checking them in and out of the pool. Then we could check out the
// connection at the start of the request.
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

func CreateUser(conn *sqlite.Conn, name string, passwordHashAndSalt *HashAndSalt) (*User, error) {
	query := "insert into user (name, password_salt, password_hash) values (?, ?, ?)"

	err := sqlitex.Exec(conn, query, nil, name, passwordHashAndSalt.Salt, passwordHashAndSalt.Hash)
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

func (app *App) SignUpUser(w http.ResponseWriter, r *http.Request) {
	conn := app.db.dbpool.Get(r.Context())
	defer app.db.dbpool.Put(conn)

	if r.Method != http.MethodPost {
		app.RenderTemplate(w, "sign_up.html", SignUpForm{})
		return
	}

	var form SignUpForm
	if err := form.ParseFrom(r); err != nil {
		badRequest(w)
		return
	}

	// Max length for username and password
	const maxLength = 256

	if len(form.Name) == 0 {
		form.PushError("name", "Name is required")
	} else if len(form.Name) > maxLength {
		form.PushError("name", fmt.Sprintf("Name is too long (max %d characters)", maxLength))
	} else {
		existingUserWithName, err := GetUserByName(conn, form.Name)
		if err != nil {
			errorResponse(w, err)
			return
		}
		if existingUserWithName != nil {
			form.PushError("name", "A user with this name already exists")
		}
	}

	if len(form.Password) == 0 {
		form.PushError("password", "Password is required")
	} else if len(form.Password) > maxLength {
		form.PushError("password", fmt.Sprintf("Password is too long (max %d characters)", maxLength))
	}

	if len(form.Errors) > 0 {
		app.RenderTemplate(w, "sign_up.html", form)
		return
	}

	hashAndSalt, err := HashAndSaltPassword([]byte(form.Password))
	if err != nil {
		errorResponse(w, err)
		return
	}
	_, err = CreateUser(conn, form.Name, hashAndSalt)

	if err != nil {
		errorResponse(w, err)
		return
	}

	url := fmt.Sprintf("/user?name=%s", url.QueryEscape(form.Name))
	http.Redirect(w, r, url, http.StatusSeeOther)
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
	mux.HandleFunc("POST /posts/new", app.NewPost)
	mux.Handle("/static/", http.StripPrefix("/static", http.FileServer(http.Dir("./static"))))
	http.ListenAndServe(":7777", mux)
}
