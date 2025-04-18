// Runs the entropy.social server

package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	mathrand "math/rand"
	"net/http"
	"net/url"
	"sort"
	"time"

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

func timer(name string) func() {
	start := time.Now()
	return func() {
		duration := time.Since(start)
		log.Printf("%s took %s", name, duration)
	}
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

// It occurs to me that if I had this do nothing when err == nil, then I could do
// ```
// defer errorResponse(w, &err)
// ```
// to return 500 on any errors. But that feels like a real invitation to confusion.
func errorResponse(w http.ResponseWriter, err error) {
	log.Printf("sending 500 error: %s", err)
	http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
}

type userCtxKeyType struct{}

var userCtxKey = userCtxKeyType{}

// Adds the requesting user (if they're logged in) to the request context
func (app *App) withUserContextMiddleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn := app.db.Get(r.Context())
		defer app.db.Put(conn)
		user, err := entropy.GetUserIfLoggedIn(conn, r)
		if err != nil {
			errorResponse(w, err)
			return
		}
		ctx := r.Context()
		ctx = context.WithValue(ctx, userCtxKey, user)
		r = r.WithContext(ctx)
		h.ServeHTTP(w, r)
	})
}

// Get the requesting user (stashed on the Context by withUserContextMiddleware)
func getCurrentUser(ctx context.Context) *entropy.User {
	user, ok := ctx.Value(userCtxKey).(*entropy.User)
	// Being verbose to acknowledge that this wil be nil, and that's OK, if the user
	// isn't logged in
	if !ok {
		return nil
	}
	return user
}

func (app *App) RenderTemplate(w http.ResponseWriter, name string, data any) {
	err := app.renderer.ExecuteTemplate(w, name, data)
	if err != nil {
		errorResponse(w, err)
	}
}

type homepage struct {
	User        *entropy.User
	Posts       []entropy.Post
	NextPageURL string
	CSRFField   template.HTML
}

const timeQueryParamLayout = "20060102T150405"

// A time in the future, so that we don't filter on time at all
func defaultBefore() time.Time {
	return time.Now().UTC().Add(time.Hour)
}

// Get recommended posts, based on the ENTROPYCH, INC. CHAOS RECOMMENDATION ALGORITHM
func getRecommendedPosts(conn *sqlite.Conn, user *entropy.User, before time.Time, limit int) ([]entropy.Post, error) {
	if user == nil {
		return entropy.GetRecentPosts(conn, before, limit)
	}
	var posts []entropy.Post
	followedPosts, err := entropy.GetRecentPostsFromFollowedUsers(conn, user.UserID, before, limit)
	if err != nil {
		return nil, err
	}
	chaosPosts, err := entropy.GetRecentPosts(conn, before, limit)
	if err != nil {
		return nil, err
	}
	posts = make([]entropy.Post, 0, limit)
	for range limit {
		if len(followedPosts) == 0 && len(chaosPosts) == 0 {
			break
		}
		var takeFollow bool
		if len(followedPosts) == 0 {
			takeFollow = false
		} else if len(chaosPosts) == 0 {
			takeFollow = true
		} else {
			takeFollow = mathrand.Float32() > 0.4
		}
		if takeFollow {
			posts = append(posts, followedPosts[0])
			followedPosts = followedPosts[1:]
		} else {
			posts = append(posts, chaosPosts[0])
			chaosPosts = chaosPosts[1:]
		}
	}
	sort.Slice(posts, func(i, j int) bool {
		return posts[i].CreatedAt.After(posts[j].CreatedAt)
	})
	err = entropy.DistortPostsForUser(conn, user, posts)
	if err != nil {
		return nil, err
	}
	return posts, err
}

func (app *App) Homepage(w http.ResponseWriter, r *http.Request) {
	conn := app.db.Get(r.Context())
	defer app.db.Put(conn)
	before := defaultBefore()
	beforeRaw := r.URL.Query().Get("before")
	var err error
	if beforeRaw != "" {
		// Just ignore errors here
		before, err = time.Parse(timeQueryParamLayout, beforeRaw)
		if err != nil {
			before = defaultBefore()
		}
	}
	user := getCurrentUser(r.Context())
	posts, err := getRecommendedPosts(conn, user, before, 50)
	if err != nil {
		errorResponse(w, err)
		return
	}
	page := &homepage{
		User:      user,
		Posts:     posts,
		CSRFField: csrf.TemplateField(r),
	}
	if len(posts) > 0 {
		lastPostCreatedAt := posts[len(posts)-1].CreatedAt.UTC().Format(timeQueryParamLayout)
		page.NextPageURL = fmt.Sprintf("/?before=%s", url.QueryEscape(lastPostCreatedAt))
	}
	app.RenderTemplate(w, "index.html", page)
}

func (app *App) About(w http.ResponseWriter, r *http.Request) {
	app.RenderTemplate(w, "about.html", nil)
}

type userPostsPage struct {
	LoggedInUser           *entropy.User
	PostingUser            *entropy.User
	Posts                  []entropy.Post
	IsFollowingPostingUser bool
	PostingUserFollowStats *entropy.UserFollowStats
	DistanceFromUser       int
	CSRFField              template.HTML
}

func getUserPostsPage(conn *sqlite.Conn, user *entropy.User, postingUser *entropy.User) (*userPostsPage, error) {
	isFollowing := false
	distanceFromUser := 0
	var err error
	if user != nil && postingUser.UserID != user.UserID {
		// TODO: I could consolidate these queries
		isFollowing, err = entropy.IsFollowing(conn, user.UserID, postingUser.UserID)
		if err != nil {
			return nil, err
		}
		// Maybe just debug info
		distances, err := entropy.GetDistanceFromUser(conn, user.UserID, []int64{postingUser.UserID})
		if err != nil {
			return nil, err
		}
		distanceFromUser = distances[postingUser.UserID]
	}
	posts, err := entropy.GetRecentPostsFromUser(conn, postingUser.UserID, 50)
	if err != nil {
		return nil, err
	}
	err = entropy.DistortPostsForUser(conn, user, posts)
	if err != nil {
		return nil, err
	}
	stats, err := entropy.GetUserFollowStats(conn, postingUser.UserID)
	if err != nil {
		return nil, err
	}
	return &userPostsPage{
		LoggedInUser:           user,
		PostingUser:            postingUser,
		Posts:                  posts,
		IsFollowingPostingUser: isFollowing,
		PostingUserFollowStats: stats,
		DistanceFromUser:       distanceFromUser,
	}, nil
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
	user := getCurrentUser(r.Context())
	page, err := getUserPostsPage(conn, user, postingUser)
	if err != nil {
		errorResponse(w, err)
		return
	}
	page.CSRFField = csrf.TemplateField(r)
	app.RenderTemplate(w, "user_posts.html", page)
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

func (f *nameAndPasswordForm) pushError(fieldName string, errorMessage string) {
	f.Errors[fieldName] = append(f.Errors[fieldName], errorMessage)
}

type SignUpForm struct {
	nameAndPasswordForm
}

func newSignUpForm(r *http.Request) *SignUpForm {
	return &SignUpForm{nameAndPasswordForm{
		CSRFField: csrf.TemplateField(r),
		Errors:    make(map[string][]string),
	}}
}

func (f *SignUpForm) Validate(conn *sqlite.Conn) error {
	// Max length for username and password
	const maxLength = 256

	if len(f.Name) == 0 {
		f.pushError("name", "Name is required")
	} else if len(f.Name) > maxLength {
		f.pushError("name", fmt.Sprintf("Name is too long (max %d characters)", maxLength))
	} else {
		existingUserWithName, err := entropy.GetUserByName(conn, f.Name)
		if err != nil {
			return err
		}
		if existingUserWithName != nil {
			f.pushError("name", "A user with this name already exists")
		}
	}

	if len(f.Password) == 0 {
		f.pushError("password", "Password is required")
	} else if len(f.Password) > maxLength {
		f.pushError("password", fmt.Sprintf("Password is too long (max %d characters)", maxLength))
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
	// TODO: move to db.go?
	query := "select user_id, user_name, password_salt, password_hash from user where user_name = ? limit 1"
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
		form.pushError("name", "There is no user with this name")
		return nil, nil
	}
	if !entropy.CheckPassword([]byte(form.Password), hashAndSalt) {
		form.pushError("password", "This password is incorrect")
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

func redirectToLogin(w http.ResponseWriter, r *http.Request) {
	// Clear the session cookie in case it has expired
	entropy.ClearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (app *App) LogOut(w http.ResponseWriter, r *http.Request) {
	entropy.ClearSessionCookie(w)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (app *App) NewPost(w http.ResponseWriter, r *http.Request) {
	conn := app.db.Get(r.Context())
	defer app.db.Put(conn)

	user := getCurrentUser(r.Context())
	if user == nil {
		redirectToLogin(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		badRequest(w)
		return
	}
	content := r.PostForm.Get("content")
	// should empty posts be allowed?
	_, err := entropy.CreatePost(conn, user.UserID, content)
	if err != nil {
		errorResponse(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (app *App) FollowUser(w http.ResponseWriter, r *http.Request) {
	conn := app.db.Get(r.Context())
	defer app.db.Put(conn)

	user := getCurrentUser(r.Context())
	if user == nil {
		redirectToLogin(w, r)
		return
	}

	username := r.PathValue("username")
	followedUser, err := entropy.GetUserByName(conn, username)
	if err != nil {
		errorResponse(w, err)
		return
	}
	if followedUser == nil || user.UserID == followedUser.UserID {
		http.NotFound(w, r)
		return
	}

	err = entropy.FollowUser(conn, user.UserID, followedUser.UserID)
	if err != nil {
		errorResponse(w, err)
		return
	}

	http.Redirect(w, r, followedUser.URL(), http.StatusSeeOther)
}

// lol, a lot of duplication here
func (app *App) UnfollowUser(w http.ResponseWriter, r *http.Request) {
	conn := app.db.Get(r.Context())
	defer app.db.Put(conn)

	user := getCurrentUser(r.Context())
	if user == nil {
		redirectToLogin(w, r)
		return
	}

	username := r.PathValue("username")
	followedUser, err := entropy.GetUserByName(conn, username)
	if err != nil {
		errorResponse(w, err)
		return
	}
	if followedUser == nil || user.UserID == followedUser.UserID {
		http.NotFound(w, r)
		return
	}

	err = entropy.UnfollowUser(conn, user.UserID, followedUser.UserID)
	if err != nil {
		errorResponse(w, err)
		return
	}

	http.Redirect(w, r, followedUser.URL(), http.StatusSeeOther)
}

func main() {
	t := timer("startup")
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

	// TODO: Maybe I should wrap these handlers somehow so that they can just return an
	// error, instead of calling errorResponse for every possible 500
	mux.HandleFunc("GET /{$}", app.Homepage)
	mux.HandleFunc("GET /about", app.About)

	mux.HandleFunc("GET /signup", app.SignUpUser)
	mux.HandleFunc("POST /signup", app.SignUpUser)
	mux.HandleFunc("GET /login", app.LogIn)
	mux.HandleFunc("POST /login", app.LogIn)
	mux.HandleFunc("POST /logout", app.LogOut)

	mux.HandleFunc("POST /posts/new", app.NewPost)

	mux.HandleFunc("/u/{username}/{$}", app.ShowUserPosts)
	mux.HandleFunc("POST /u/{username}/follow", app.FollowUser)
	mux.HandleFunc("POST /u/{username}/unfollow", app.UnfollowUser)

	csrfProtect := csrf.Protect(secretKey, csrf.FieldName("csrf_token"))
	server := csrfProtect(app.withUserContextMiddleware(mux))
	t()

	http.ListenAndServe(":7777", server)
}
