package entropy

import (
	"context"
	"crypto/rand"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"time"

	"crawshaw.io/sqlite"
	"crawshaw.io/sqlite/sqlitex"
)

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
	UserID int64
	Name   string
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
	PostID    int64
	UserName  string
	CreatedAt time.Time
	Content   string
}

func (p *Post) UserURL() string {
	return userURL(p.UserName)
}

type UserSession struct {
	UserID          int64
	SessionPublicID []byte
	ExpirationTime  time.Time
}

const defaultSessionDuration time.Duration = time.Minute * 30

func utcNow() time.Time {
	return time.Now().UTC()
}

func GetRecentPosts(conn *sqlite.Conn, limit int) ([]Post, error) {
	var posts []Post
	query := `
		select post_id, user.name, created_at, content
		from post
		join user using (user_id)
		order by created_at desc
		limit ?`
	collect := func(stmt *sqlite.Stmt) error {
		post := Post{
			PostID:    stmt.ColumnInt64(0),
			UserName:  stmt.ColumnText(1),
			CreatedAt: time.Unix(stmt.ColumnInt64(2), 0),
			Content:   stmt.ColumnText(3),
		}
		posts = append(posts, post)
		return nil
	}
	err := sqlitex.Exec(conn, query, collect, limit)
	return posts, err
}

func GetRecentPostsFromUser(conn *sqlite.Conn, userID int64, limit int) ([]Post, error) {
	var posts []Post
	query := `
		select post_id, user.name, created_at, content
		from post
		join user using (user_id)
		where user_id = ?
		order by created_at desc
		limit ?`
	collect := func(stmt *sqlite.Stmt) error {
		post := Post{
			PostID:    stmt.ColumnInt64(0),
			UserName:  stmt.ColumnText(1),
			CreatedAt: time.Unix(stmt.ColumnInt64(2), 0),
			Content:   stmt.ColumnText(3),
		}
		posts = append(posts, post)
		return nil
	}
	err := sqlitex.Exec(conn, query, collect, userID, limit)
	return posts, err
}

func GetUserByName(conn *sqlite.Conn, name string) (*User, error) {
	var user *User = nil
	query := "select user_id, name from user where name = ? limit 1"
	collect := func(stmt *sqlite.Stmt) error {
		user = &User{
			UserID: stmt.ColumnInt64(0),
			Name:   stmt.ColumnText(1),
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
		select post_id, user.name, created_at, content
		from post
		join user using (user_id)
		where post_id = ?`

	collect := func(stmt *sqlite.Stmt) error {
		post = &Post{
			PostID:    stmt.ColumnInt64(0),
			UserName:  stmt.ColumnText(1),
			CreatedAt: time.Unix(stmt.ColumnInt64(2), 0),
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

	query := "insert into user (name, password_salt, password_hash) values (?, ?, ?)"
	err = sqlitex.Exec(conn, query, nil, name, hashAndSalt.Salt, hashAndSalt.Hash)
	if err != nil {
		return nil, err
	}

	userID := conn.LastInsertRowID()
	user := &User{UserID: userID, Name: name}
	return user, err
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
		select user_id, user.name
		from user_session
		join user using (user_id)
		where session_public_id = ? and expiration_time > ?`
	var user *User
	collect := func(stmt *sqlite.Stmt) error {
		user = &User{
			UserID: stmt.ColumnInt64(0),
			Name:   stmt.ColumnText(1),
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
		)
		select
			following_count,
			follower_count
		from follows
		join followers using (user_id)
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
