package entropy

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"time"

	"crawshaw.io/sqlite"
	"crawshaw.io/sqlite/sqlitex"
)

// I wonder if I should have a "Must"-style helper for these SQLite query functions that
// I really truly never expect to error. Might simplify some server code.

// The SQL schema for the app's database
//
//go:embed schema.sql
var schemaSQL string

// It seems like a lot of go code ties the connection to the context (and passes around
// the context), rather than passing around the actual connection. But I'm happy with
// passing around the connection (for now, at least).
//
// I'm curious if there's a way that I can make the db error handling nicer. We don't
// want to panic, but we also don't expect them to happen almost ever.
//
// ...Maybe I _do_ want to panic on unexpected SQLite errors?
type DB struct {
	*sqlitex.Pool
}

func setUpDb(conn *sqlite.Conn) error {
	return sqlitex.ExecScript(conn, schemaSQL)
}

// Like sqlitex.Exec but you pass a function that binds the parameters of the function, instead of
func exec(conn *sqlite.Conn, query string, resultFn func(stmt *sqlite.Stmt) error, bindFn func(stmt *sqlite.Stmt) error) error {
	stmt, err := conn.Prepare(query)
	if err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	if err = bindFn(stmt); err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	for {
		hasRow, err := stmt.Step()
		if err != nil {
			return fmt.Errorf("exec: %w", err)
		}
		if !hasRow {
			break
		}
		if resultFn != nil {
			if err := resultFn(stmt); err != nil {
				if err, isError := err.(sqlite.Error); isError {
					if err.Loc == "" {
						err.Loc = "Exec"
					} else {
						err.Loc = "Exec: " + err.Loc
					}
				}
				// don't modify non-Error errors from resultFn.
				return err
			}
		}
	}
	resetErr := stmt.Reset()
	if err == nil {
		err = resetErr
	}
	return err
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

	return &DB{dbpool}, nil
}

type User struct {
	// I use int64s because that's what SQLite returns under the hood. But it would be
	// fine to use plain int, surely
	UserID      int64
	Name        string
	DisplayName string
	// We don't always need the Bio. Should I take it out of the User struct?
	Bio string
}

func (u *User) Exists() bool {
	return u.UserID > 0
}

func (u *User) URL() string {
	return userURL(u.Name)
}

func userURL(userName string) string {
	return fmt.Sprintf("/u/%s/", url.PathEscape(userName))
}

type Post struct {
	PostID          int64
	UserID          int64
	UserName        string
	UserDisplayName string
	CreatedAt       time.Time
	Content         string
}

func (p *Post) UserURL() string {
	return userURL(p.UserName)
}

type UserSession struct {
	UserID          int64
	SessionPublicID []byte
	ExpirationTime  time.Time
}

const defaultSessionDuration time.Duration = time.Hour * 48

func utcNow() time.Time {
	return time.Now().UTC()
}

func GetDistanceFromUser(conn *sqlite.Conn, userID int64, otherUserIDs []int64) (map[int64]int, error) {
	// This could **almost** be a recursive CTE, but since our graph has cycles we need
	// to exclude everything from the previous iteration with a `not in ( ... )` clause
	// over the current set of rows (and that's not allowed).
	//
	// I still feel like there must be a better way to do this.
	//
	// Could also add some limit clauses if heavily-followed users become an issue
	query := `
		with follows as (
			select followed_user_id as user_id, 1 as distance
			from user_follow
			where user_id = :userID
		),
		follows2 as (
			select distinct user_follow.followed_user_id as user_id, 2 as distance
			from follows
			join user_follow using (user_id)
			where
				user_follow.followed_user_id not in (select user_id from follows)
				and user_follow.followed_user_id != :userID
		),
		follows3 as (
			select distinct user_follow.followed_user_id as user_id, 3 as distance
			from follows2
			join user_follow using (user_id)
			where
				user_follow.followed_user_id not in (select user_id from follows)
				and user_follow.followed_user_id not in (select user_id from follows2)
				and user_follow.followed_user_id != :userID
		),
		follows4 as (
			select distinct user_follow.followed_user_id as user_id, 4 as distance
			from follows3
			join user_follow using (user_id)
			where
				user_follow.followed_user_id not in (select user_id from follows)
				and user_follow.followed_user_id not in (select user_id from follows2)
				and user_follow.followed_user_id not in (select user_id from follows3)
				and user_follow.followed_user_id != :userID
		),
		other_users as (
			/* dumb hack to pass an array through. */
			select value as user_id from json_each(:otherUserIDsJSON)
		)
		select * from follows
		where user_id in (select user_id from other_users)
		union all
		select * from follows2
		where user_id in (select user_id from other_users)
		union all
		select * from follows3
		where user_id in (select user_id from other_users)
		union all
		select * from follows4
		where user_id in (select user_id from other_users)
	`
	otherUserIDsJSON, err := json.Marshal(otherUserIDs)
	if err != nil {
		return nil, err
	}
	result := make(map[int64]int)
	collect := func(stmt *sqlite.Stmt) error {
		u := stmt.ColumnInt64(0)
		if _, in := result[u]; in {
			return fmt.Errorf("user ID %d returned more than once", u)
		}
		result[u] = stmt.ColumnInt(1)
		return nil
	}
	err = exec(conn, query, collect, func(stmt *sqlite.Stmt) error {
		stmt.SetInt64(":userID", userID)
		stmt.SetText(":otherUserIDsJSON", string(otherUserIDsJSON))
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, otherUserID := range otherUserIDs {
		_, ok := result[otherUserID]
		if !ok {
			result[otherUserID] = MaxDistortionLevel
		}
	}
	return result, err
}

func GetRecentPosts(conn *sqlite.Conn, before time.Time, limit int) ([]Post, error) {
	posts := make([]Post, 0, limit)
	query := `
		select post_id, user.user_id, user.user_name, user.display_name, post.created_at, post.content
		from post
		join user using (user_id)
		where post.created_at < ?
		order by created_at desc
		limit ?`
	collect := func(stmt *sqlite.Stmt) error {
		post := Post{
			PostID:          stmt.ColumnInt64(0),
			UserID:          stmt.ColumnInt64(1),
			UserName:        stmt.ColumnText(2),
			UserDisplayName: stmt.ColumnText(3),
			CreatedAt:       time.Unix(stmt.ColumnInt64(4), 0).UTC(),
			Content:         stmt.ColumnText(5),
		}
		posts = append(posts, post)
		return nil
	}
	err := sqlitex.Exec(conn, query, collect, before.UTC().Unix(), limit)
	return posts, err
}

func GetRecentPostsFromFollowedUsers(conn *sqlite.Conn, userID int64, before time.Time, limit int) ([]Post, error) {
	posts := make([]Post, 0, limit)
	query := `
		with followed_users as (
			select followed_user_id
			from user_follow
			where user_id = :userID
		)
		select post_id, user.user_id, user.user_name, user.display_name, post.created_at, post.content
		from post
		join user using (user_id)
		where (
			user.user_id in (select followed_user_id from followed_users)
			or user.user_id = :userID
		) and post.created_at < :before
		order by created_at desc
		limit :limit
		`
	collect := func(stmt *sqlite.Stmt) error {
		post := Post{
			PostID:          stmt.ColumnInt64(0),
			UserID:          stmt.ColumnInt64(1),
			UserName:        stmt.ColumnText(2),
			UserDisplayName: stmt.ColumnText(3),
			CreatedAt:       time.Unix(stmt.ColumnInt64(4), 0).UTC(),
			Content:         stmt.ColumnText(5),
		}
		// fmt.Printf("followed post: %v\n", post)
		posts = append(posts, post)
		return nil
	}
	err := exec(conn, query, collect, func(stmt *sqlite.Stmt) error {
		stmt.SetInt64(":userID", userID)
		stmt.SetInt64(":before", before.UTC().Unix())
		stmt.SetInt64(":limit", int64(limit))
		return nil
	})
	return posts, err
}

// Get recent posts from users that userID does not follow
func GetRecentPostsFromRandos(conn *sqlite.Conn, userID int64, before time.Time, limit int) ([]Post, error) {
	posts := make([]Post, 0, limit)
	query := `
		with followed_users as (
			select followed_user_id
			from user_follow
			where user_id = :userID
		)
		select post_id, user.user_id, user.user_name, user.display_name, post.created_at, post.content
		from post
		join user using (user_id)
		where post.created_at < :before
			and user.user_id not in (select followed_user_id from followed_users)
			and user.user_id != :userID
		order by created_at desc
		limit :limit`
	collect := func(stmt *sqlite.Stmt) error {
		post := Post{
			PostID:          stmt.ColumnInt64(0),
			UserID:          stmt.ColumnInt64(1),
			UserName:        stmt.ColumnText(2),
			UserDisplayName: stmt.ColumnText(3),
			CreatedAt:       time.Unix(stmt.ColumnInt64(4), 0).UTC(),
			Content:         stmt.ColumnText(5),
		}
		// fmt.Printf("rando post: %v\n", post)
		posts = append(posts, post)
		return nil
	}
	err := exec(conn, query, collect, func(stmt *sqlite.Stmt) error {
		stmt.SetInt64(":userID",
			userID)
		stmt.SetInt64(":before", before.UTC().Unix())
		stmt.SetInt64(":limit", int64(limit))
		return nil
	})
	return posts, err
}

func GetRecentPostsFromUser(conn *sqlite.Conn, userID int64, limit int) ([]Post, error) {
	var posts []Post
	query := `
		select post_id, user.user_name, user.display_name, post.created_at, post.content
		from post
		join user using (user_id)
		where user_id = ?
		order by created_at desc
		limit ?`
	collect := func(stmt *sqlite.Stmt) error {
		post := Post{
			PostID:          stmt.ColumnInt64(0),
			UserID:          userID,
			UserName:        stmt.ColumnText(1),
			UserDisplayName: stmt.ColumnText(2),
			CreatedAt:       time.Unix(stmt.ColumnInt64(3), 0).UTC(),
			Content:         stmt.ColumnText(4),
		}
		posts = append(posts, post)
		return nil
	}
	err := sqlitex.Exec(conn, query, collect, userID, limit)
	return posts, err
}

func GetUserByName(conn *sqlite.Conn, name string) (*User, error) {
	var user *User = nil
	query := "select user_id, user_name, display_name, bio from user where user_name = ? limit 1"
	collect := func(stmt *sqlite.Stmt) error {
		user = &User{
			UserID:      stmt.ColumnInt64(0),
			Name:        stmt.ColumnText(1),
			DisplayName: stmt.ColumnText(2),
			Bio:         stmt.ColumnText(3),
		}
		return nil
	}
	err := sqlitex.Exec(conn, query, collect, name)
	return user, err
}

func CreatePost(conn *sqlite.Conn, userID int64, content string) (*Post, error) {
	query := "insert into post (user_id, created_at, content) values (?, ?, ?)"
	err := sqlitex.Exec(conn, query, nil, userID, utcNow().Unix(), content)
	if err != nil {
		return nil, err
	}
	postID := conn.LastInsertRowID()

	var post *Post
	query = `
		select post_id, user.user_name, created_at, content
		from post
		join user using (user_id)
		where post_id = ?`

	collect := func(stmt *sqlite.Stmt) error {
		post = &Post{
			PostID:    stmt.ColumnInt64(0),
			UserName:  stmt.ColumnText(1),
			CreatedAt: time.Unix(stmt.ColumnInt64(2), 0).UTC(),
			Content:   stmt.ColumnText(3),
		}
		return nil
	}
	err = sqlitex.Exec(conn, query, collect, postID)
	return post, err
}

func CreateUser(conn *sqlite.Conn, name string, password string) (*User, error) {
	hashAndSalt, err := HashAndSaltPassword([]byte(password))
	if err != nil {
		return nil, err
	}

	query := "insert into user (user_name, display_name, password_salt, password_hash) values (?, ?, ?, ?)"
	err = sqlitex.Exec(conn, query, nil, name, name, hashAndSalt.Salt, hashAndSalt.Hash)
	if err != nil {
		return nil, err
	}

	userID := conn.LastInsertRowID()
	user := &User{UserID: userID, Name: name, DisplayName: name}
	return user, err
}

func UpdateUserProfile(conn *sqlite.Conn, name string, displayName string, bio string) error {
	query := `
		update user
		set display_name = :displayName, bio = :bio
		where user_name = :name`
	return exec(conn, query, nil, func(stmt *sqlite.Stmt) error {
		stmt.SetText(":displayName", displayName)
		stmt.SetText(":bio", bio)
		stmt.SetText(":name", name)
		return nil
	})
}

func CreateUserSession(conn *sqlite.Conn, userID int64) (*UserSession, error) {
	sessionPublicID := make([]byte, 128)
	if _, err := rand.Read(sessionPublicID); err != nil {
		return nil, err
	}
	now := utcNow()
	expirationTime := now.Add(defaultSessionDuration)
	query := `
		insert into user_session (user_id, session_public_id, created_at, expiration_time)
		values (?, ?, ?, ?)`
	err := sqlitex.Exec(conn, query, nil, userID, sessionPublicID, now.Unix(), expirationTime.Unix())
	if err != nil {
		return nil, err
	}
	session := &UserSession{
		UserID:          userID,
		SessionPublicID: sessionPublicID,
		ExpirationTime:  expirationTime,
	}
	return session, err
}

func GetUserFromSessionPublicID(conn *sqlite.Conn, sessionPublicID []byte) (*User, error) {
	query := `
		select user_id, user.user_name, user.display_name, user.bio
		from user_session
		join user using (user_id)
		where session_public_id = ? and expiration_time > ?`
	var user *User
	collect := func(stmt *sqlite.Stmt) error {
		user = &User{
			UserID:      stmt.ColumnInt64(0),
			Name:        stmt.ColumnText(1),
			DisplayName: stmt.ColumnText(2),
			Bio:         stmt.ColumnText(3),
		}
		return nil
	}
	err := sqlitex.Exec(conn, query, collect, sessionPublicID, utcNow().Unix())
	return user, err
}

func FollowUser(conn *sqlite.Conn, userID int64, followedUserID int64) error {
	if userID == followedUserID {
		return fmt.Errorf("userID %d cannot follow itself", userID)
	}
	query := `
		insert into user_follow (user_id, followed_user_id, followed_at)
		values (?, ?, ?)
		on conflict do nothing`
	return sqlitex.Exec(conn, query, nil, userID, followedUserID, utcNow().Unix())
}

func UnfollowUser(conn *sqlite.Conn, userID int64, followedUserID int64) error {
	query := "delete from user_follow where user_id = ? and followed_user_id = ?"
	return sqlitex.Exec(conn, query, nil, userID, followedUserID)
}

func IsFollowing(conn *sqlite.Conn, userID int64, followedUserID int64) (bool, error) {
	query := "select 1 from user_follow where user_id = ? and followed_user_id = ?"
	isFollowing := false
	collect := func(stmt *sqlite.Stmt) error {
		if stmt.ColumnInt64(0) == 1 {
			isFollowing = true
		}
		return nil
	}
	err := sqlitex.Exec(conn, query, collect, userID, followedUserID)
	return isFollowing, err
}

type UserFollowStats struct {
	UserID         int64
	FollowingCount int64
	FollowerCount  int64
}

func GetUserFollowStats(conn *sqlite.Conn, userID int64) (*UserFollowStats, error) {
	query := `
		with follows as (
			select user_id, count(*) as following_count
			from user_follow
			where user_id = ?
			group by user_id
		),
		followers as (
			select followed_user_id as user_id, count(*) as follower_count
			from user_follow
			where followed_user_id = ?
			group by followed_user_id
		),
		both as (
			select
				following_count,
				follower_count
			from follows
			left join followers using (user_id)
			union /* hack to do a full outer join */
			select
				following_count,
				follower_count
			from followers
			left join follows using (user_id)
		)
		select *
		from both
		limit 1
		`
	stats := &UserFollowStats{UserID: userID}
	collect := func(stmt *sqlite.Stmt) error {
		stats.FollowingCount = stmt.ColumnInt64(0)
		stats.FollowerCount = stmt.ColumnInt64(1)
		return nil
	}
	err := sqlitex.Exec(conn, query, collect, userID, userID)
	return stats, err
}

// Maybe take the distances as an argument, instead of looking them up here
func DistortPostsForUser(conn *sqlite.Conn, user *User, posts []Post) error {
	if user == nil {
		// TODO: decide what the default distortion level should be for unauthenticated.
		// Maybe just maximum? (5)
		for i := range posts {
			posts[i].Content = DistortContent(posts[i].Content, MaxDistortionLevel)
		}
		return nil
	}

	// Distort the posts based on how close the users are to us in the follower graph
	userIDSet := make(map[int64]bool)
	for i := range posts {
		if posts[i].UserID != user.UserID {
			userIDSet[posts[i].UserID] = true
		}
	}
	otherUserIDs := make([]int64, 0, len(userIDSet))
	for k := range userIDSet {
		otherUserIDs = append(otherUserIDs, k)
	}
	distances, err := GetDistanceFromUser(conn, user.UserID, otherUserIDs)
	if err != nil {
		return err
	}
	for i := range posts {
		// No distortion for your own posts
		if posts[i].UserID == user.UserID {
			continue
		}
		posts[i].Content = DistortContent(posts[i].Content, distances[posts[i].UserID])
	}
	return nil
}

// TODO: streaming blob writes? would be cool!
// TODO: maybe this function should manage setting a unique name, instead of expecting
// that from the caller. (and let filename not be unique)
func SaveUpload(conn *sqlite.Conn, filename string, contentType string, contents []byte) error {
	query := "insert into upload (filename, content_type, contents) values (?, ?, ?)"
	return sqlitex.Exec(conn, query, nil, filename, contentType, contents)
}

func OpenUploadContents(conn *sqlite.Conn, filename string) (io.Reader, error) {
	query := "select upload_id from upload where filename = ? limit 1"
	var uploadID int
	collect := func(stmt *sqlite.Stmt) error {
		uploadID = stmt.ColumnInt(0)
		return nil
	}
	if err := sqlitex.Exec(conn, query, collect, filename); err != nil {
		return nil, err
	}
	return conn.OpenBlob("", "upload", "contents", int64(uploadID), false)
}
