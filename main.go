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
)

type User struct {
	UserID int64
	Name   string
}

func (u *User) Exists() bool {
	return u.UserID > 0
}

type DB struct {
	dbpool *sqlitex.Pool
}

func (db *DB) GetRecentPosts(ctx context.Context, limit int) ([]Post, error) {
	conn := db.dbpool.Get(ctx)
	defer db.dbpool.Put(conn)

	var posts []Post = make([]Post, 0)

	err := sqlitex.Exec(
		conn,
		"select post_id, user_id, created_at, content from post order by created_at desc limit ?",
		func(stmt *sqlite.Stmt) error {
			post := Post{
				PostID:    stmt.ColumnInt64(0),
				UserID:    stmt.ColumnInt64(1),
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
	UserID    int64
	CreatedAt time.Time
	Content   string
}

func (db *DB) CreatePost(ctx context.Context, userID int64, content string) (*Post, error) {
	conn := db.dbpool.Get(ctx)
	defer db.dbpool.Put(conn)

	var post *Post = nil

	err := sqlitex.Exec(conn, "insert into post (user_id, created_at, content) values (?, ?, ?);", nil, userID, time.Now().UTC().Unix(), content)
	if err != nil {
		return nil, err
	}
	postID := conn.LastInsertRowID()

	err = sqlitex.Exec(conn, "select post_id, user_id, created_at, content from post where post_id = ?;", func(stmt *sqlite.Stmt) error {
		post = &Post{
			PostID:    stmt.ColumnInt64(0),
			UserID:    stmt.ColumnInt64(1),
			CreatedAt: time.Unix(stmt.ColumnInt64(2), 0),
			Content:   stmt.ColumnText(3),
		}
		return nil
	}, postID)

	return post, err
}

func (db *DB) CreateUser(ctx context.Context, name string) (*User, error) {
	conn := db.dbpool.Get(ctx)
	defer db.dbpool.Put(conn)

	var user *User = nil

	err := sqlitex.Exec(conn, "insert into user (name) values (?);", nil, name)
	if err != nil {
		return nil, err
	}
	userID := conn.LastInsertRowID()
	user = &User{UserID: userID, Name: name}
	return user, err
}

type App struct {
	templates *template.Template
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

func NewDB() (*DB, error) {
	dbpool, err := sqlitex.Open("test.db", 0, 10)
	if err != nil {
		return nil, err
	}

	conn := dbpool.Get(context.TODO())
	if conn == nil {
		return nil, errors.New("couldn't get a connection")
	}
	defer dbpool.Put(conn)

	err = setUpDb(conn)
	if err != nil {
		return nil, fmt.Errorf("couldn't set up db: %s", err)
	}

	return &DB{dbpool: dbpool}, nil
}

func NewApp(db *DB) *App {
	return &App{
		templates: template.Must(template.ParseGlob("templates/*")),
		db:        db,
	}
}

func errorResponse(w http.ResponseWriter, err error) {
	log.Printf("sending 500 error: %s", err)
	http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
}

func (app *App) RenderTemplate(w http.ResponseWriter, name string, data any) {
	err := app.templates.ExecuteTemplate(w, name, data)
	if err != nil {
		errorResponse(w, err)
	}
}

func (app *App) homepage(w http.ResponseWriter, r *http.Request) {
	posts, err := app.db.GetRecentPosts(r.Context(), 10)
	if err != nil {
		errorResponse(w, err)
	}
	app.RenderTemplate(w, "index.html", posts)
}

func (app *App) helloUser(w http.ResponseWriter, r *http.Request) {
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

func (app *App) createUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		app.RenderTemplate(w, "create.html", nil)
		return
	}
	r.ParseForm()
	name := r.PostForm.Get("name")
	// TODO: validation
	_, err := app.db.CreateUser(r.Context(), name)
	if err != nil {
		errorResponse(w, err)
		return
	}
	url := fmt.Sprintf("/user?name=%s", url.QueryEscape(name))
	http.Redirect(w, r, url, http.StatusSeeOther)
}

func (app *App) createPost(w http.ResponseWriter, r *http.Request) {
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
	// TODO: validation
	_, err = app.db.CreatePost(r.Context(), user.UserID, content)
	if err != nil {
		errorResponse(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func main() {
	db, err := NewDB()
	if err != nil {
		log.Fatal(err)
	}
	app := NewApp(db)

	mux := http.NewServeMux()
	mux.HandleFunc("/", app.homepage)
	mux.HandleFunc("/user", app.helloUser)
	mux.HandleFunc("GET /users/new", app.createUser)
	mux.HandleFunc("POST /users/new", app.createUser)
	mux.HandleFunc("POST /posts/new", app.createPost)
	mux.Handle("/static/", http.StripPrefix("/static", http.FileServer(http.Dir("./static"))))
	http.ListenAndServe(":7777", mux)
}
