package main

import (
	"context"
	"crypto/rand"
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

func (db *DB) GetRecentPosts(ctx context.Context, limit int) ([]Post, error) {
	conn := db.dbpool.Get(ctx)
	defer db.dbpool.Put(conn)

	var posts []Post = make([]Post, 0)

	err := sqlitex.Exec(
		conn,
		`select post_id, user.name, created_at, content
		from post
		join user using (user_id)
		order by created_at desc
		limit ?`,
		func(stmt *sqlite.Stmt) error {
			post := Post{
				PostID:    stmt.ColumnInt64(0),
				UserName:  stmt.ColumnText(1),
				CreatedAt: time.Unix(stmt.ColumnInt64(2), 0),
				Content:   stmt.ColumnText(3),
			}
			posts = append(posts, post)
			return nil
		},
		limit,
	)
	return posts, err
}

func (db *DB) GetUserByName(ctx context.Context, name string) (*User, error) {
	conn := db.dbpool.Get(ctx)
	defer db.dbpool.Put(conn)

	var user *User = nil

	err := sqlitex.Exec(
		conn,
		"select user_id, name from user where name = ? limit 1",
		func(stmt *sqlite.Stmt) error {
			user = &User{
				UserID: stmt.ColumnInt64(0),
				Name:   stmt.ColumnText(1),
			}
			return nil
		},
		name,
	)
	return user, err
}

type Post struct {
	PostID    int64
	UserName  string
	CreatedAt time.Time
	Content   string
}

func (db *DB) CreatePost(ctx context.Context, userID int64, content string) (*Post, error) {
	conn := db.dbpool.Get(ctx)
	defer db.dbpool.Put(conn)

	var post *Post = nil

	err := sqlitex.Exec(conn, "insert into post (user_id, created_at, content) values (?, ?, ?)", nil, userID, time.Now().UTC().Unix(), content)
	if err != nil {
		return nil, err
	}
	postID := conn.LastInsertRowID()

	err = sqlitex.Exec(
		conn,
		`select post_id, user.name, created_at, content
		from post
		join user using (user_id)
		where post_id = ?`,
		func(stmt *sqlite.Stmt) error {
			post = &Post{
				PostID:    stmt.ColumnInt64(0),
				UserName:  stmt.ColumnText(1),
				CreatedAt: time.Unix(stmt.ColumnInt64(2), 0),
				Content:   stmt.ColumnText(3),
			}
			return nil
		},
		postID,
	)

	return post, err
}

func (db *DB) CreateUser(ctx context.Context, name string, passwordSalt []byte, passwordHash []byte) (*User, error) {
	conn := db.dbpool.Get(ctx)
	defer db.dbpool.Put(conn)

	var user *User = nil

	err := sqlitex.Exec(conn, "insert into user (name, password_salt, password_hash) values (?, ?, ?)", nil, name, passwordSalt, passwordHash)
	if err != nil {
		return nil, err
	}
	userID := conn.LastInsertRowID()
	user = &User{UserID: userID, Name: name}
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
	posts, err := app.db.GetRecentPosts(r.Context(), 10)
	if err != nil {
		errorResponse(w, err)
		return
	}
	app.RenderTemplate(w, "index.html", posts)
}

func (app *App) HelloUser(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	name := q.Get("name")
	if name == "" {
		name = "Max"
	}
	user, err := app.db.GetUserByName(r.Context(), name)
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
	Errors   map[string][]string
}

func (f *SignUpForm) PushError(fieldName string, errorMessage string) {
	f.Errors[fieldName] = append(f.Errors[fieldName], errorMessage)
}

func (app *App) SignUpUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		app.RenderTemplate(w, "sign_up.html", SignUpForm{})
		return
	}
	// Max length for username and password
	const maxLength = 256

	if err := r.ParseForm(); err != nil {
		errorResponse(w, err)
		return
	}
	form := SignUpForm{
		Name:     r.PostForm.Get("name"),
		Password: r.PostForm.Get("password"),
		Errors:   make(map[string][]string),
	}

	if len(form.Name) == 0 {
		form.PushError("name", "Name is required")
	} else if len(form.Name) > maxLength {
		form.PushError("name", fmt.Sprintf("Name is too long (max %d characters)", maxLength))
	} else {
		existingUserWithName, err := app.db.GetUserByName(r.Context(), form.Name)
		if err != nil {
			errorResponse(w, err)
			return
		}
		if existingUserWithName != nil {
			form.PushError("name", "A user with this name already exists")
		}
	}

	if len(form.Name) == 0 {
		form.PushError("password", "Password is required")
	} else if len(form.Name) > maxLength {
		form.PushError("password", fmt.Sprintf("Password is too long (max %d characters)", maxLength))
	}

	if len(form.Errors) > 0 {
		app.RenderTemplate(w, "sign_up.html", form)
		return
	}

	salt := make([]byte, 32)
	_, err := rand.Read(salt)
	if err != nil {
		errorResponse(w, err)
		return
	}
	hash := HashPassword([]byte(form.Password), salt)
	_, err = app.db.CreateUser(r.Context(), form.Name, salt, hash)

	if err != nil {
		errorResponse(w, err)
		return
	}

	url := fmt.Sprintf("/user?name=%s", url.QueryEscape(form.Name))
	http.Redirect(w, r, url, http.StatusSeeOther)
}

func (app *App) NewPost(w http.ResponseWriter, r *http.Request) {
	// TODO: authentication and stuff
	user, err := app.db.GetUserByName(r.Context(), "Max")
	if err != nil {
		errorResponse(w, err)
		return
	}
	if err = r.ParseForm(); err != nil {
		errorResponse(w, err)
		return
	}
	content := r.PostForm.Get("content")
	// should empty posts be allowed?
	_, err = app.db.CreatePost(r.Context(), user.UserID, content)
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
