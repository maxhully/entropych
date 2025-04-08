package main

import (
	"context"
	"fmt"
	"html/template"
	"log"
	"net/http"

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

	err = sqlitex.ExecScript(conn, `
		create table if not exists user (
			user_id integer primary key not null,
			name text not null
		);
	`)
	if err != nil {
		log.Fatalf("couldn't set up db: %s", err)
	}

	return &App{
		templates: template.Must(template.ParseGlob("templates/*")),
		dbpool:    dbpool,
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
		http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
		return
	}

	if user == nil {
		user = &User{
			Id:   -1,
			Name: name,
		}
	}
	err = app.templates.ExecuteTemplate(w, "hello.html", user)
	if err != nil {
		log.Fatalf("executing template: %s", err)
	}
}

func (app *App) createUser(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		r.ParseForm()
		name := r.PostForm.Get("name")

		conn := app.dbpool.Get(r.Context())
		defer app.dbpool.Put(conn)

		err := sqlitex.Exec(conn, "insert into user (name) values (?);", nil, name)
		if err != nil {
			log.Printf("error: %s", err)
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, "/", http.StatusSeeOther)
	} else {
		err := app.templates.ExecuteTemplate(w, "create.html", nil)
		if err != nil {
			log.Fatalf("executing template: %s", err)
		}
	}
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
