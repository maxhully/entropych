package main

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
)

type User struct {
	Name string
}

func helloWorld(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "Hello, world!")
}

type App struct {
	templates *template.Template
}

func NewApp() *App {
	return &App{
		templates: template.Must(template.ParseGlob("templates/*")),
	}
}

func (app *App) helloUser(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	name := q.Get("name")
	if name == "" {
		name = "Max"
	}
	u := User{Name: name}
	err := app.templates.ExecuteTemplate(w, "hello.html", u)
	if err != nil {
		log.Fatalf("executing template: %s", err)
	}
}

func main() {
	app := NewApp()

	mux := http.NewServeMux()
	mux.HandleFunc("/", helloWorld)
	mux.HandleFunc("/user", app.helloUser)
	mux.Handle("/static/", http.StripPrefix("/static", http.FileServer(http.Dir("./static"))))
	http.ListenAndServe(":7777", mux)
}
