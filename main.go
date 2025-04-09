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

	"crawshaw.io/sqlite"
	"crawshaw.io/sqlite/sqlitex"
)

type User struct {
	UserID int64
	Name   string
}

func (u *User) Exists() bool {
	return u.UserID != -1
}

func helloWorld(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "Hello, world!")
}

type DB struct {
	dbpool *sqlitex.Pool
}

func (db *DB) GetUserByName(ctx context.Context, name string) (*User, error) {
	conn := db.dbpool.Get(ctx)
	defer db.dbpool.Put(conn)

	var user *User
	user = nil

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

func (db *DB) CreateUser(ctx context.Context, name string) (*User, error) {
	conn := db.dbpool.Get(ctx)
	defer db.dbpool.Put(conn)

	var user *User
	user = nil

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
			UserID: -1,
			Name:   name,
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

func main() {
	db, err := NewDB()
	if err != nil {
		log.Fatal(err)
	}
	app := NewApp(db)

	mux := http.NewServeMux()
	mux.HandleFunc("/", helloWorld)
	mux.HandleFunc("/user", app.helloUser)
	mux.HandleFunc("/users/new", app.createUser)
	mux.Handle("/static/", http.StripPrefix("/static", http.FileServer(http.Dir("./static"))))
	http.ListenAndServe(":7777", mux)
}
