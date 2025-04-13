package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"

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

func errorResponse(w http.ResponseWriter, err error) {
	log.Printf("sending 500 error: %s", err)
	http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
}

func (app *App) RenderTemplate(w http.ResponseWriter, name string, data any) {
	// TODO: once I have html error pages, I could set this:
	// w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// (right now we might send plain text if there's an error)
	err := app.renderer.ExecuteTemplate(w, name, data)
	if err != nil {
		errorResponse(w, err)
	}
}

type homepage struct {
	User      *entropy.User
	Posts     []entropy.Post
	CSRFField template.HTML
}

func (app *App) Homepage(w http.ResponseWriter, r *http.Request) {
	conn := app.db.Get(r.Context())
	defer app.db.Put(conn)

	user, err := entropy.GetUserIfLoggedIn(conn, r)
	if err != nil {
		errorResponse(w, err)
		return
	}
	posts, err := entropy.GetRecentPosts(conn, 10)
	for i := range posts {
		posts[i].Content = entropy.DistortContent(posts[i].Content, 1)
	}
	if err != nil {
		errorResponse(w, err)
		return
	}
	app.RenderTemplate(w, "index.html", homepage{User: user, Posts: posts, CSRFField: csrf.TemplateField(r)})
}

type userPostsPage struct {
	LoggedInUser *entropy.User
	PostingUser  *entropy.User
	Posts        []entropy.Post
}

func (app *App) ShowUserPosts(w http.ResponseWriter, r *http.Request) {
	conn := app.db.Get(r.Context())
	defer app.db.Put(conn)

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

	// TODO: do we need this?
	user, err := entropy.GetUserIfLoggedIn(conn, r)
	if err != nil {
		errorResponse(w, err)
		return
	}

	posts, err := entropy.GetRecentPostsFromUser(conn, postingUser.UserID, 10)
	for i := range posts {
		// TODO: figure out distance from user
		posts[i].Content = entropy.DistortContent(posts[i].Content, 1)
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

type SignUpForm struct {
	nameAndPasswordForm
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
		existingUserWithName, err := entropy.GetUserByName(conn, f.Name)
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
	conn := app.db.Get(r.Context())
	defer app.db.Put(conn)

	form := newSignUpForm(r)
	if r.Method != http.MethodPost {
		app.RenderTemplate(w, "signup.html", form)
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
	query := "select user_id, name, password_salt, password_hash from user where name = ? limit 1"
	collect := func(stmt *sqlite.Stmt) error {
		var err error
		user = &entropy.User{
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
	if !entropy.CheckPassword([]byte(form.Password), hashAndSalt) {
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

func (app *App) LogIn(w http.ResponseWriter, r *http.Request) {
	conn := app.db.Get(r.Context())
	defer app.db.Put(conn)

	form := newLogInForm(r)
	if r.Method != http.MethodPost {
		app.RenderTemplate(w, "login.html", form)
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

func badRequest(w http.ResponseWriter) {
	http.Error(w, "400 Bad Request", http.StatusBadRequest)
}

func (app *App) NewPost(w http.ResponseWriter, r *http.Request) {
	conn := app.db.Get(r.Context())
	defer app.db.Put(conn)

	user, err := entropy.GetUserIfLoggedIn(conn, r)
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
	_, err = entropy.CreatePost(conn, user.UserID, content)
	if err != nil {
		errorResponse(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
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
	db, err := entropy.NewDB(dbpool)
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
