package main

import (
	"context"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"

	"crawshaw.io/sqlite"
	"crawshaw.io/sqlite/sqlitex"
)

type User struct {
	Id   int
	Name string
}

func (u *User) Exists() bool {
	return u.Id != -1
}

func helloWorld(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "Hello, world!")
}

type App struct {
	templates *template.Template
	dbpool    *sqlitex.Pool
}

func setUpDb(conn *sqlite.Conn) error {
	sqlBytes, err := os.ReadFile("schema.sql")
	if err != nil {
		return err
	}
	sql := string(sqlBytes)
	return sqlitex.ExecScript(conn, sql)
}

func NewApp() *App {
	dbpool, err := sqlitex.Open("test.db", 0, 10)
	if err != nil {
		log.Fatal(err)
	}

	conn := dbpool.Get(context.TODO())
	if conn == nil {
		log.Fatal("I couldn't get a connection!")
	}
	defer dbpool.Put(conn)

	err = setUpDb(conn)
	if err != nil {
		log.Fatalf("couldn't set up db: %s", err)
	}

	return &App{
		templates: template.Must(template.ParseGlob("templates/*")),
		dbpool:    dbpool,
	}
}

func errorResponse(w http.ResponseWriter, err error) {
	log.Printf("sending 500 error: %s", err)
	http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
}

func (app *App) RenderTemplate(w http.ResponseWriter, name string, data any) {
	err := app.templates.ExecuteTemplate(w, "create.html", nil)
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

	conn := app.dbpool.Get(r.Context())
	defer app.dbpool.Put(conn)

	var user *User
	user = nil

	err := sqlitex.Exec(conn, "select user_id, name from user where name = ? limit 1", func(stmt *sqlite.Stmt) error {
		user = &User{
			Id:   stmt.ColumnInt(0),
			Name: stmt.ColumnText(1),
		}
		return nil
	}, name)
	if err != nil {
		errorResponse(w, err)
		return
	}

	if user == nil {
		user = &User{
			Id:   -1,
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

	conn := app.dbpool.Get(r.Context())
	defer app.dbpool.Put(conn)

	err := sqlitex.Exec(conn, "insert into user (name) values (?);", nil, name)
	if err != nil {
		log.Printf("error: %s", err)
		errorResponse(w, err)
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func main() {
	app := NewApp()

	mux := http.NewServeMux()
	mux.HandleFunc("/", helloWorld)
	mux.HandleFunc("/user", app.helloUser)
	mux.HandleFunc("/users/new", app.createUser)
	mux.Handle("/static/", http.StripPrefix("/static", http.FileServer(http.Dir("./static"))))
	http.ListenAndServe(":7777", mux)
}
