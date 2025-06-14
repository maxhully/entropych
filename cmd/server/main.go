// Runs the entropy.social server
//
// This file has all the HTTP endpoints in it. It's a bit messy, and you could imagine
// splitting it up into smaller "services" for posts, users, uploads, etc. But if I were
// to do that right now, I think I'd only be doing it to prove that I could, and to
// chase some sort of "working in a clean white room" aesthetic. And experience has
// taught me to ignore that impulse.

package main

import (
	"bytes"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"crawshaw.io/sqlite"
	"crawshaw.io/sqlite/sqlitex"
	"github.com/gorilla/csrf"
	"github.com/gorilla/handlers"
	"github.com/maxhully/entropy"
	"github.com/maxhully/entropy/avatargen"
	"golang.org/x/crypto/acme/autocert"
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

func badRequest(w http.ResponseWriter, err error) {
	log.Printf("sending 400 error: %s", err)
	http.Error(w, "400 Bad Request", http.StatusBadRequest)
}

func redirectToLogin(w http.ResponseWriter, r *http.Request) {
	// Clear the session cookie in case it has expired
	entropy.ClearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusFound)
}

func (app *App) RenderTemplate(w http.ResponseWriter, r *http.Request, name string, data any) {
	err := app.renderer.ExecuteTemplate(w, r, name, data)
	if err != nil {
		errorResponse(w, err)
	}
}

type homepage struct {
	User        *entropy.User
	Posts       []entropy.Post
	NextPageURL string
}

const timeQueryParamLayout = "20060102T150405"

// A time in the future, so that we don't filter on time at all
func defaultBefore() time.Time {
	return time.Now().UTC().Add(time.Hour)
}

// Parse the "before" time query parameter from the given request. If we can't parse it
// (either because it's missing or malformed), we return defaultBefore() instead
func parseBefore(r *http.Request) time.Time {
	beforeRaw := r.URL.Query().Get("before")
	var err error
	if beforeRaw == "" {
		return defaultBefore()
	}
	// Just ignore errors here
	before, err := time.Parse(timeQueryParamLayout, beforeRaw)
	if err != nil {
		return defaultBefore()
	}
	return before
}

// Parse the "after" time query parameter from the given request. If we can't parse it
// (either because it's missing or malformed), we return defaultBefore() instead
func parseAfter(r *http.Request) time.Time {
	afterRaw := r.URL.Query().Get("after")
	var err error
	if afterRaw == "" {
		return time.Time{}
	}
	// Just ignore errors here
	after, err := time.Parse(timeQueryParamLayout, afterRaw)
	if err != nil {
		return time.Time{}
	}
	return after
}

// Default limit when paginating posts
const postsLimit = 50

func (app *App) Homepage(w http.ResponseWriter, r *http.Request) {
	conn := app.db.GetReadOnly(r.Context())
	defer app.db.PutReadOnly(conn)
	before := parseBefore(r)
	user := entropy.GetCurrentUser(r.Context())
	posts, err := entropy.GetRecommendedPosts(conn, user, before, postsLimit)
	if err != nil {
		errorResponse(w, err)
		return
	}
	page := &homepage{
		User:        user,
		Posts:       posts,
		NextPageURL: getNextPageURL(posts, "/", postsLimit),
	}
	app.RenderTemplate(w, r, "index.html", page)
}

func getNextPageURL(posts []entropy.Post, urlPath string, limit int) string {
	if len(posts) == limit {
		lastPostCreatedAt := posts[len(posts)-1].CreatedAt.UTC().Format(timeQueryParamLayout)
		return fmt.Sprintf("%s?before=%s", urlPath, url.QueryEscape(lastPostCreatedAt))
	}
	return ""
}

func (app *App) About(w http.ResponseWriter, r *http.Request) {
	app.RenderTemplate(w, r, "about.html", nil)
}

type userPostsPage struct {
	LoggedInUser           *entropy.User
	PostingUser            *entropy.User
	Posts                  []entropy.Post
	IsFollowingPostingUser bool
	PostingUserFollowStats *entropy.UserFollowStats
	DistanceFromUser       int
	NextPageURL            string
}

func getUserPostsPage(conn *sqlite.Conn, user *entropy.User, postingUser *entropy.User, before time.Time) (*userPostsPage, error) {
	isFollowing := false
	distanceFromUser := entropy.MaxDistortionLevel
	var err error
	if user != nil && postingUser.UserID != user.UserID {
		distances, err := entropy.GetDistanceFromUser(conn, user.UserID, []int64{postingUser.UserID})
		if err != nil {
			return nil, err
		}
		distanceFromUser = distances[postingUser.UserID]
		isFollowing = distances[postingUser.UserID] == 1
	}
	if user != nil && postingUser.UserID == user.UserID {
		distanceFromUser = 0
	}
	posts, err := entropy.GetRecentPostsFromUser(conn, postingUser.UserID, before, postsLimit)
	if err != nil {
		return nil, err
	}
	if err := entropy.DecoratePosts(conn, user, posts); err != nil {
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
		NextPageURL:            getNextPageURL(posts, postingUser.URL(), postsLimit),
	}, nil
}

func (app *App) ShowUserPosts(w http.ResponseWriter, r *http.Request) {
	conn := app.db.GetReadOnly(r.Context())
	defer app.db.PutReadOnly(conn)

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
	user := entropy.GetCurrentUser(r.Context())
	before := parseBefore(r)
	page, err := getUserPostsPage(conn, user, postingUser, before)
	if err != nil {
		errorResponse(w, err)
		return
	}
	app.RenderTemplate(w, r, "user_posts.html", page)
}

type nameAndPasswordForm struct {
	Name     string
	Password string
	// rename to Problems?
	Errors map[string]string
}

func (f *nameAndPasswordForm) ParseFromBody(r *http.Request) error {
	// TODO: this should never error, because the CSRF middleware should already have
	// parsed the body. So I should be able to move this into newSignUpForm, etc.
	r.ParseForm()
	f.Name = r.PostForm.Get("name")
	f.Password = r.PostForm.Get("password")
	return nil
}

type SignUpForm struct {
	nameAndPasswordForm
}

func newSignUpForm() *SignUpForm {
	return &SignUpForm{nameAndPasswordForm{
		Errors: make(map[string]string),
	}}
}

func (f *SignUpForm) Validate(conn *sqlite.Conn) error {
	// Max length for username and password
	const maxLength = 128

	if len(f.Name) == 0 {
		f.Errors["name"] = "Name is required"
	} else if len(f.Name) > maxLength {
		f.Errors["name"] = fmt.Sprintf("Name is too long (max %d characters)", maxLength)
	} else if strings.ContainsFunc(f.Name, unicode.IsSpace) {
		f.Errors["name"] = "Name must not have any spaces in it"
	} else {
		existingUserWithName, err := entropy.GetUserByName(conn, f.Name)
		if err != nil {
			return err
		}
		if existingUserWithName != nil {
			f.Errors["name"] = "A user with this name already exists"
		}
	}

	if len(f.Password) == 0 {
		f.Errors["password"] = "Password is required"
	} else if len(f.Password) > maxLength {
		f.Errors["password"] = fmt.Sprintf("Password is too long (max %d characters)", maxLength)
	}
	return nil
}

func (app *App) SignUpUser(w http.ResponseWriter, r *http.Request) {
	// TODO: handle when user is already logged in
	conn := app.db.Get(r.Context())
	defer app.db.Put(conn)
	var err error
	defer sqlitex.Save(conn)(&err)

	form := newSignUpForm()
	if r.Method != http.MethodPost {
		app.RenderTemplate(w, r, "signup.html", form)
		return
	}
	if err := form.ParseFromBody(r); err != nil {
		badRequest(w, err)
		return
	}
	if err := form.Validate(conn); err != nil {
		errorResponse(w, err)
		return
	}
	if len(form.Errors) > 0 {
		app.RenderTemplate(w, r, "signup.html", form)
		return
	}
	user, err := entropy.CreateUser(conn, form.Name, form.Password)
	if err != nil {
		errorResponse(w, err)
		return
	}
	buf := new(bytes.Buffer)
	if err = avatargen.GenerateAvatarPNG(buf); err != nil {
		errorResponse(w, err)
		return
	}
	uploadID, err := entropy.SaveUpload(conn, "image/png", buf.Bytes())
	if err != nil {
		errorResponse(w, err)
		return
	}
	if err = entropy.UpdateUserProfile(conn, user.Name, "", "", uploadID); err != nil {
		errorResponse(w, err)
		return
	}
	session, err := entropy.CreateUserSession(conn, user.UserID)
	if err != nil {
		errorResponse(w, err)
		return
	}
	http.SetCookie(w, session.ToCookie())

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
			// Omitting DisplayName, Bio, and AvatarUploadID because we don't need them here
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
		form.Errors["name"] = "There is no user with this name"
		return nil, nil
	}
	if !entropy.CheckPassword([]byte(form.Password), hashAndSalt) {
		form.Errors["password"] = "This password is incorrect"
		return nil, nil
	}
	// The user can log in!
	return user, nil
}

func newLogInForm() LogInForm {
	return LogInForm{nameAndPasswordForm{
		Errors: make(map[string]string),
	}}
}

func (app *App) LogIn(w http.ResponseWriter, r *http.Request) {
	conn := app.db.Get(r.Context())
	defer app.db.Put(conn)
	form := newLogInForm()
	if r.Method != http.MethodPost {
		app.RenderTemplate(w, r, "login.html", form)
		return
	}
	if err := form.ParseFromBody(r); err != nil {
		badRequest(w, err)
		return
	}
	user, err := checkLogInForm(conn, &form)
	if err != nil {
		errorResponse(w, err)
		return
	}
	if user == nil {
		app.RenderTemplate(w, r, "login.html", form)
		return
	}
	session, err := entropy.CreateUserSession(conn, user.UserID)
	if err != nil {
		errorResponse(w, err)
		return
	}
	http.SetCookie(w, session.ToCookie())
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (app *App) LogOut(w http.ResponseWriter, r *http.Request) {
	sessionPublicID, err := entropy.GetSessionPublicIdFromCookie(r)
	if err != nil {
		errorResponse(w, err)
		return
	}
	if sessionPublicID == nil {
		// Not logged in; just do nothing
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	conn := app.db.Get(r.Context())
	defer app.db.Put(conn)
	entropy.ClearSessionCookie(w)
	if err = entropy.ExpireSession(conn, sessionPublicID); err != nil {
		errorResponse(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (app *App) NewPost(w http.ResponseWriter, r *http.Request) {
	conn := app.db.Get(r.Context())
	defer app.db.Put(conn)

	user := entropy.GetCurrentUser(r.Context())
	if user == nil {
		redirectToLogin(w, r)
		return
	}
	r.ParseForm()
	content := r.PostForm.Get("content")
	// should empty posts be allowed?
	_, err := entropy.CreatePost(conn, user.UserID, content)
	if err != nil {
		errorResponse(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

type postPage struct {
	Post           *entropy.Post
	User           *entropy.User // the logged-in user
	Replies        []entropy.Post
	ReplyingToPost *entropy.Post
	NextPageURL    string // The URL for the next page of replies, if there are any
}

func getPostPage(conn *sqlite.Conn, user *entropy.User, postID int64, repliesAfter time.Time) (*postPage, error) {
	page := postPage{User: user}
	var err error
	{
		post, err := entropy.GetPost(conn, postID)
		if err != nil {
			return nil, err
		}
		if post == nil {
			return nil, nil
		}
		// is there a better way to transmute a pointer into a length-1 slice?
		postSlice := []entropy.Post{*post}
		// TODO: could I consolidate these three DecoratePosts calls?
		if err := entropy.DecoratePosts(conn, user, postSlice); err != nil {
			return nil, err
		}
		// I'm curious about whether this is actually still the original address of `post`
		page.Post = &postSlice[0]
	}
	// TODO: better abstraction around pagination...
	if page.Replies, err = entropy.GetPostReplies(conn, postID, repliesAfter, postsLimit); err != nil {
		return nil, err
	}
	if len(page.Replies) == postsLimit {
		lastPostCreatedAt := page.Replies[len(page.Replies)-1].CreatedAt.UTC().Format(timeQueryParamLayout)
		page.NextPageURL = fmt.Sprintf("%s?after=%s", page.Post.PostURL(), url.QueryEscape(lastPostCreatedAt))
	}
	if err := entropy.DecoratePosts(conn, user, page.Replies); err != nil {
		return nil, err
	}
	if page.Post.ReplyingToPostID != 0 {
		if page.ReplyingToPost, err = entropy.GetPost(conn, page.Post.ReplyingToPostID); err != nil {
			return nil, err
		}
		parentPostSlice := []entropy.Post{*page.ReplyingToPost}
		if err := entropy.DecoratePosts(conn, user, parentPostSlice); err != nil {
			return nil, err
		}
		page.ReplyingToPost = &parentPostSlice[0]
	}
	return &page, err
}

func (app *App) ShowPost(w http.ResponseWriter, r *http.Request) {
	conn := app.db.Get(r.Context())
	defer app.db.Put(conn)
	postID, err := strconv.Atoi(r.PathValue("post_id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	page, err := getPostPage(conn, entropy.GetCurrentUser(r.Context()), int64(postID), parseAfter(r))
	if err != nil {
		errorResponse(w, err)
		return
	}
	// TODO: this template
	app.RenderTemplate(w, r, "show_post.html", page)
}

func (app *App) ReplyToPost(w http.ResponseWriter, r *http.Request) {
	conn := app.db.Get(r.Context())
	defer app.db.Put(conn)
	postID, err := strconv.Atoi(r.PathValue("post_id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	user := entropy.GetCurrentUser(r.Context())
	if user == nil {
		redirectToLogin(w, r)
		return
	}
	r.ParseForm()
	content := r.PostForm.Get("content")
	replyPostID, err := entropy.ReplyToPost(conn, int64(postID), user.UserID, content)
	if err != nil {
		errorResponse(w, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/p/%d/", replyPostID), http.StatusSeeOther)
}

func (app *App) ReactToPost(w http.ResponseWriter, r *http.Request) {
	conn := app.db.Get(r.Context())
	defer app.db.Put(conn)
	user := entropy.GetCurrentUser(r.Context())
	if user == nil {
		redirectToLogin(w, r)
		return
	}
	postID, err := strconv.Atoi(r.PathValue("post_id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	foundPost, err := entropy.ReactToPostIfExists(conn, user.UserID, int64(postID), "❤️")
	if err != nil {
		errorResponse(w, err)
		return
	}
	if !foundPost {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/p/%d/", postID), http.StatusSeeOther)
}

func (app *App) UnreactToPost(w http.ResponseWriter, r *http.Request) {
	conn := app.db.Get(r.Context())
	defer app.db.Put(conn)
	user := entropy.GetCurrentUser(r.Context())
	if user == nil {
		redirectToLogin(w, r)
		return
	}
	postID, err := strconv.Atoi(r.PathValue("post_id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	foundPost, err := entropy.UnreactToPostIfExists(conn, user.UserID, int64(postID))
	if err != nil {
		errorResponse(w, err)
		return
	}
	if !foundPost {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/p/%d/", postID), http.StatusSeeOther)
}

func (app *App) FollowUser(w http.ResponseWriter, r *http.Request) {
	conn := app.db.Get(r.Context())
	defer app.db.Put(conn)

	user := entropy.GetCurrentUser(r.Context())
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

	user := entropy.GetCurrentUser(r.Context())
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

// TODO: reset password and stuff
type updateProfileForm struct {
	DisplayName string
	Bio         string
	Errors      map[string]string
}

type updateProfilePage struct {
	Form updateProfileForm
	User *entropy.User
}

func (f *updateProfileForm) Validate() {
	if len(f.DisplayName) > 256 {
		f.Errors["display_name"] = fmt.Sprintf("Display name is too long (max %d characters)", 256)
	}
	if len(f.Bio) > 256 {
		f.Errors["bio"] = fmt.Sprintf("Bio is too long (max %d characters)", 256)
	}
}

func validateUpload(header *multipart.FileHeader) error {
	contentTypes := header.Header["Content-Type"]
	if len(contentTypes) != 1 {
		return fmt.Errorf("unexpected mime header (zero or >1 content types?): %+v", header)
	}
	contentType := contentTypes[0]
	if contentType != "image/png" {
		return fmt.Errorf("expected contentType to be image/png, got %q", contentType)
	}
	return nil
}

func (app *App) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	var err error
	conn := app.db.Get(r.Context())
	defer app.db.Put(conn)
	// Save so that if the update fails we don't create the upload
	defer sqlitex.Save(conn)(&err)

	user := entropy.GetCurrentUser(r.Context())
	if user == nil {
		redirectToLogin(w, r)
		return
	}
	var page updateProfilePage
	page.User = user
	page.Form.Errors = make(map[string]string)
	page.Form.DisplayName = user.DisplayName
	page.Form.Bio = user.Bio
	if r.Method == http.MethodGet {
		app.RenderTemplate(w, r, "user_profile.html", page)
		return
	}

	// Handling POST now
	if err := r.ParseMultipartForm(maxRequestBytes); err != nil {
		if _, ok := err.(*http.MaxBytesError); ok {
			http.Error(w, "413 request entity too large", http.StatusRequestEntityTooLarge)
			return
		}
		errorResponse(w, err)
		return
	}
	if v := r.PostForm.Get("display_name"); v != "" {
		page.Form.DisplayName = v
	}
	// Allow for an empty bio
	if r.PostForm.Has("bio") {
		page.Form.Bio = r.PostForm.Get("bio")
	}
	// TODO: validate avatar upload (must be .png)
	if page.Form.Validate(); len(page.Form.Errors) > 0 {
		app.RenderTemplate(w, r, "user_profile.html", page)
		return
	}

	var uploadID int64
	file, header, err := r.FormFile("avatar")
	if errors.Is(err, http.ErrMissingFile) || errors.Is(err, http.ErrNotMultipart) {
		uploadID = 0
	} else if err != nil {
		badRequest(w, err)
		return
	} else {
		defer r.MultipartForm.RemoveAll()
		defer func() {
			if file != nil {
				file.Close()
			}
		}()
		// Handle uploaded file in this else block.
		if err = validateUpload(header); err != nil {
			page.Form.Errors["avatar"] = "Avatar must be a .png image."
			app.RenderTemplate(w, r, "user_profile.html", page)
			return
		}
		var contents []byte
		if contents, err = io.ReadAll(file); err != nil {
			errorResponse(w, err)
			return
		}
		contentTypes := header.Header["Content-Type"]
		if len(contentTypes) != 1 {
			badRequest(w, fmt.Errorf("unexpected mime header (zero or >1 content types?): %+v", header))
			return
		}
		contentType := contentTypes[0]
		if uploadID, err = entropy.SaveUpload(conn, contentType, contents); err != nil {
			errorResponse(w, err)
			return
		}
	}
	err = entropy.UpdateUserProfile(conn, user.Name, page.Form.DisplayName, page.Form.Bio, uploadID)
	if err != nil {
		errorResponse(w, err)
		return
	}
	http.Redirect(w, r, user.URL(), http.StatusSeeOther)
}

func (app *App) ServeUpload(w http.ResponseWriter, r *http.Request) {
	conn := app.db.GetReadOnly(r.Context())
	defer app.db.PutReadOnly(conn)
	parts := strings.SplitN(r.PathValue("upload_id"), ".", 2)
	if len(parts) != 2 || strings.ToLower(parts[1]) != "png" {
		http.NotFound(w, r)
		return
	}
	uploadID, err := strconv.Atoi(parts[0])
	if err != nil {
		http.NotFound(w, r)
		return
	}
	blob, contentType, err := entropy.OpenUploadContents(conn, int64(uploadID))
	if err != nil {
		errorResponse(w, err)
		return
	}
	defer blob.Close()
	w.Header().Set("Content-Type", contentType)
	// Set a 1-year expiration for the PNGs, because they're immutable
	w.Header().Set("Cache-Control", "max-age=31536000, public, immutable")
	// TODO: should probably buffer this and handle errors?
	io.Copy(w, blob)
}

func withSafeHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Scheme == "https" {
			w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'self'; block-all-mixed-content; object-src 'none'")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		// TODO: Permissions-Policy?
		h.ServeHTTP(w, r)
	})
}

const maxRequestBytes = 1024 * 1024

// TODO: could convert devMode/behindProxy/listenTLS to a "mode" enum with 3 options
type Config struct {
	secretKey   []byte
	dbUri       string
	devMode     bool
	behindProxy bool
	listenTLS   bool   // listen on ports 80 and 443 and serve TLS using autocert
	addr        string // address to listen on, if not in devMode and not serving TLS
}

func parseConfig() Config {
	secretKeyHex := os.Getenv("ENTROPYCH_SECRET_KEY")
	dbUri := os.Getenv("ENTROPYCH_DB")
	// The address to listen on
	addr := os.Getenv("ENTROPYCH_ADDR")
	_, behindProxy := os.LookupEnv("ENTROPYCH_BEHIND_PROXY")
	if secretKeyHex == "" {
		log.Fatal("ENTROPYCH_SECRET_KEY is required")
	}
	secretKey, err := hex.DecodeString(secretKeyHex)
	if err != nil {
		log.Fatal("ENTROPYCH_SECRET_KEY must be hex-encoded")
	}
	if len(secretKey) != 32 {
		log.Fatal("ENTROPYCH_SECRET_KEY must be 32 bytes")
	}
	if dbUri == "" {
		log.Fatal("ENTROPYCH_DB is required")
	}

	devMode := flag.Bool("dev", false, "Run in dev mode (listen on localhost, with plain HTTP)")
	flag.Parse()

	if !(*devMode) && addr == "" {
		log.Fatal("ENTROPYCH_ADDR is required when not in dev mode")
	}
	if behindProxy && addr == ":443" {
		log.Fatalf("ENTROPYCH_BEHIND_PROXY cannot be true when ENTROPYCH_ADDR=%q", addr)
	}
	if (*devMode) && addr == ":443" {
		log.Fatalf("Cannot run in dev mode and serve TLS (ENTROPYCH_ADDR=%q)", addr)
	}

	return Config{
		secretKey:   secretKey,
		dbUri:       dbUri,
		addr:        addr,
		devMode:     *devMode,
		behindProxy: behindProxy,
		listenTLS:   addr == ":443",
	}
}

func main() {
	t := timer("startup")

	conf := parseConfig()

	db, err := entropy.NewDB(conf.dbUri, 10)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	app := NewApp(db)

	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static", http.FileServer(http.Dir("./static"))))

	mux.HandleFunc("GET /{$}", app.Homepage)
	mux.HandleFunc("GET /about", app.About)

	mux.HandleFunc("GET /signup", app.SignUpUser)
	mux.HandleFunc("POST /signup", app.SignUpUser)
	mux.HandleFunc("GET /login", app.LogIn)
	mux.HandleFunc("POST /login", app.LogIn)
	mux.HandleFunc("POST /logout", app.LogOut)
	mux.HandleFunc("GET /profile", app.UpdateProfile)
	mux.HandleFunc("POST /profile", app.UpdateProfile)

	mux.HandleFunc("POST /posts/new", app.NewPost)
	mux.HandleFunc("GET /p/{post_id}/{$}", app.ShowPost)
	mux.HandleFunc("POST /p/{post_id}/react", app.ReactToPost)
	mux.HandleFunc("POST /p/{post_id}/unreact", app.UnreactToPost)
	mux.HandleFunc("POST /p/{post_id}/reply", app.ReplyToPost)

	mux.HandleFunc("/u/{username}/{$}", app.ShowUserPosts)
	mux.HandleFunc("POST /u/{username}/follow", app.FollowUser)
	mux.HandleFunc("POST /u/{username}/unfollow", app.UnfollowUser)

	mux.HandleFunc("GET /uploads/{upload_id}", app.ServeUpload)

	trustedOrigins := []string{"entropych.maxhully.net"}
	if conf.devMode {
		trustedOrigins = []string{"localhost:7777"}
	}

	var handler http.Handler
	handler = entropy.WithUserContextMiddleware(app.db, mux)
	csrfProtect := csrf.Protect(
		conf.secretKey,
		csrf.FieldName("csrf_token"),
		csrf.TrustedOrigins(trustedOrigins),
		csrf.Path("/"),
		csrf.SameSite(csrf.SameSiteStrictMode),
		csrf.HttpOnly(true),
		csrf.Secure(true),
	)
	handler = csrfProtect(handler)
	handler = handlers.CompressHandler(handler)
	handler = withSafeHeaders(handler)
	handler = http.MaxBytesHandler(handler, maxRequestBytes)
	handler = handlers.LoggingHandler(os.Stdout, handler)
	if conf.behindProxy {
		handler = handlers.ProxyHeaders(handler)
	}
	t()

	if conf.devMode {
		log.Fatal(http.ListenAndServe(":7777", handler))
	} else if conf.listenTLS {
		cacheDir := filepath.Join(os.Getenv("HOME"), ".cache", "golang-autocert")
		if err := os.MkdirAll(cacheDir, 0700); err != nil {
			log.Printf("warning: autocert.NewListener not using a cache: %v", err)
		}
		// Based on https://www.reddit.com/r/golang/comments/91qznj/cannot_get_autocert_to_work/
		certManager := autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			Cache:      autocert.DirCache(cacheDir),
			HostPolicy: autocert.HostWhitelist("entropych.maxhully.net"),
		}
		go http.ListenAndServe(":80", certManager.HTTPHandler(nil))
		server := &http.Server{
			Addr:    ":443",
			Handler: handler,
			TLSConfig: &tls.Config{
				GetCertificate: certManager.GetCertificate,
			},
		}
		log.Fatal(server.ListenAndServeTLS("", ""))
	} else {
		log.Fatal(http.ListenAndServe(conf.addr, handler))
	}
}
