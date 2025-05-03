// All the operations on the entropych database are in here.
//
// This package is called `entropy` because originally I was gonna call it
// entropy.social. But that domain name was taken. For a while it redirected to Twitter,
// which was an even better joke than entropych.social is.

package entropy

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/url"
	"time"

	"crawshaw.io/sqlite"
	"crawshaw.io/sqlite/sqlitex"
)

// The SQL schema for the app's database
//
//go:embed schema.sql
var schemaSQL string

// DB manages a pool each of read-only and read-write connections to the SQLite database.
//
// It seems like a lot of go code ties the connection to the context (and passes around
// the context), rather than passing around the actual connection. But I'm happy with
// passing around the connection (for now, at least). The alternate design would be to
// make all of these functions methods of DB, and have them acquire the right type of
// connection for the operation (read or write). That would have some advantages.
type DB struct {
	roPool *sqlitex.Pool
	rwPool *sqlitex.Pool
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

func NewDB(uri string, poolSize int) (*DB, error) {
	rwPool, err := sqlitex.Open(uri,
		(sqlite.SQLITE_OPEN_READWRITE |
			sqlite.SQLITE_OPEN_CREATE |
			sqlite.SQLITE_OPEN_WAL |
			sqlite.SQLITE_OPEN_URI |
			sqlite.SQLITE_OPEN_NOMUTEX),
		1,
	)
	if err != nil {
		return nil, fmt.Errorf("couldn't open connection pool: %s", err)
	}
	conn := rwPool.Get(context.TODO())
	if conn == nil {
		return nil, errors.New("couldn't get a connection")
	}
	defer rwPool.Put(conn)
	if err = setUpDb(conn); err != nil {
		rwPool.Close()
		return nil, fmt.Errorf("couldn't set up db: %s", err)
	}
	roPool, err := sqlitex.Open(uri,
		(sqlite.SQLITE_OPEN_READONLY |
			sqlite.SQLITE_OPEN_WAL |
			sqlite.SQLITE_OPEN_URI |
			sqlite.SQLITE_OPEN_NOMUTEX),
		poolSize,
	)
	if err != nil {
		rwPool.Close()
		return nil, fmt.Errorf("couldn't open connection pool: %s", err)
	}
	return &DB{roPool: roPool, rwPool: rwPool}, nil
}

func (db *DB) Get(ctx context.Context) *sqlite.Conn {
	return db.rwPool.Get(ctx)
}

func (db *DB) Put(conn *sqlite.Conn) {
	db.rwPool.Put(conn)
}

func (db *DB) GetReadOnly(ctx context.Context) *sqlite.Conn {
	return db.roPool.Get(ctx)
}

func (db *DB) PutReadOnly(conn *sqlite.Conn) {
	db.roPool.Put(conn)
}

func (db *DB) Close() error {
	return errors.Join(db.roPool.Close(), db.rwPool.Close())
}

type User struct {
	// I use int64s because that's what SQLite returns under the hood. But it would be
	// fine to use plain int, surely
	UserID      int64
	Name        string
	DisplayName string
	// TODO: We don't always need the Bio. Maybe I should take it out of the User struct
	Bio            string
	AvatarUploadID int64
}

func (u *User) Exists() bool {
	return u.UserID > 0
}

func (u *User) URL() string {
	return userURL(u.Name)
}

func (u *User) AvatarURL() string {
	return getUploadURL(u.AvatarUploadID)
}

func userURL(userName string) string {
	return fmt.Sprintf("/u/%s/", url.PathEscape(userName))
}

type PostReactionCount struct {
	Emoji       string
	Count       int
	UserReacted bool
}

type Post struct {
	PostID                 int64
	UserID                 int64
	UserName               string
	UserDisplayName        string
	UserAvatarUploadID     int64 // TODO: get the upload filename instead
	CreatedAt              time.Time
	Content                string
	Reactions              []PostReactionCount
	ReplyCount             int // the number of replies this post got
	ReplyingToPostID       int64
	ReplyingToPostUserName string
	DistanceFromUser       int // whether the logged in user follows the author of this post
}

func (p *Post) UserURL() string {
	return userURL(p.UserName)
}

func (p *Post) PostURL() string {
	return fmt.Sprintf("/p/%d/", p.PostID)
}

func getUploadURL(uploadID int64) string {
	if uploadID == 0 {
		return "/static/Prospero_and_miranda.jpg"
	}
	return fmt.Sprintf("/uploads/%d.png", uploadID)
}

func (p *Post) UserAvatarURL() string {
	return getUploadURL(p.UserAvatarUploadID)
}

func (p *Post) LoggedInUserIsFollowing() bool {
	return p.DistanceFromUser == 1
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

// Return a map mapping each of the otherUserIDs to their distance from userID (capped
// at MaxDistortionLevel).
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

func collectPosts(posts *[]Post) func(stmt *sqlite.Stmt) error {
	return func(stmt *sqlite.Stmt) error {
		post := Post{
			PostID:             stmt.ColumnInt64(0),
			UserID:             stmt.ColumnInt64(1),
			UserName:           stmt.ColumnText(2),
			UserDisplayName:    stmt.ColumnText(3),
			CreatedAt:          time.Unix(stmt.ColumnInt64(4), 0).UTC(),
			Content:            stmt.ColumnText(5),
			UserAvatarUploadID: stmt.ColumnInt64(6),
		}
		// fmt.Printf("followed post: %v\n", post)
		*posts = append(*posts, post)
		return nil
	}
}

// TODO: I feel like the `< :before` clauses should be <=. But we want to skip the last
// ID from the previous page. This is drifting into "opaque pagination keys" territory.
func GetRecentPosts(conn *sqlite.Conn, before time.Time, limit int) ([]Post, error) {
	posts := make([]Post, 0, limit)
	query := `
		select
			post_id,
			user.user_id,
			user.user_name,
			user.display_name,
			post.created_at,
			post.content,
			user.avatar_upload_id
		from post
		join user using (user_id)
		where post.created_at < ?
		order by created_at desc
		limit ?`
	err := sqlitex.Exec(conn, query, collectPosts(&posts), before.UTC().Unix(), limit)
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
		select
			post_id,
			user.user_id,
			user.user_name,
			user.display_name,
			post.created_at,
			post.content,
			user.avatar_upload_id
		from post
		join user using (user_id)
		where (
			user.user_id in (select followed_user_id from followed_users)
			or user.user_id = :userID
		) and post.created_at < :before
		order by created_at desc
		limit :limit
		`
	err := exec(conn, query, collectPosts(&posts), func(stmt *sqlite.Stmt) error {
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
		select
			post_id,
			user.user_id,
			user.user_name,
			user.display_name,
			post.created_at,
			post.content,
			user.avatar_upload_id
		from post
		join user using (user_id)
		where post.created_at < :before
			and user.user_id not in (select followed_user_id from followed_users)
			and user.user_id != :userID
		order by created_at desc
		limit :limit`
	err := exec(conn, query, collectPosts(&posts), func(stmt *sqlite.Stmt) error {
		stmt.SetInt64(":userID",
			userID)
		stmt.SetInt64(":before", before.UTC().Unix())
		stmt.SetInt64(":limit", int64(limit))
		return nil
	})
	return posts, err
}

func GetRecentPostsFromUser(conn *sqlite.Conn, userID int64, before time.Time, limit int) ([]Post, error) {
	var posts []Post
	query := `
		select
			post_id,
			user.user_id,
			user.user_name,
			user.display_name,
			post.created_at,
			post.content,
			user.avatar_upload_id
		from post
		join user using (user_id)
		where user_id = ?
			and created_at < ?
		order by created_at desc
		limit ?`
	err := sqlitex.Exec(conn, query, collectPosts(&posts), userID, before.UTC().Unix(), limit)
	return posts, err
}

func GetPost(conn *sqlite.Conn, postID int64) (*Post, error) {
	var posts []Post
	query := `
		select
			post_id,
			user.user_id,
			user.user_name,
			user.display_name,
			post.created_at,
			post.content,
			user.avatar_upload_id
		from post
		join user using (user_id)
		where post_id = ?`
	if err := sqlitex.Exec(conn, query, collectPosts(&posts), postID); err != nil {
		return nil, err
	}
	if len(posts) == 0 {
		return nil, nil
	}
	return &posts[0], nil
}

func GetPostReplies(conn *sqlite.Conn, postID int64, after time.Time, limit int) ([]Post, error) {
	var posts []Post
	query := `
		select
			post.post_id,
			user.user_id,
			user.user_name,
			user.display_name,
			post.created_at,
			post.content,
			user.avatar_upload_id
		from post_reply
		join post on post_reply.reply_post_id = post.post_id
		join user using (user_id)
		where post_reply.post_id = ?
			and post.created_at > ?
		order by post.created_at asc
		limit ?`
	err := sqlitex.Exec(conn, query, collectPosts(&posts), postID, after.UTC().Unix(), limit)
	return posts, err
}

func GetUserByName(conn *sqlite.Conn, name string) (*User, error) {
	var user *User = nil
	query := `
		select user_id, user_name, display_name, bio, avatar_upload_id
		from user
		where user_name = ?
		limit 1`
	collect := func(stmt *sqlite.Stmt) error {
		user = &User{
			UserID:         stmt.ColumnInt64(0),
			Name:           stmt.ColumnText(1),
			DisplayName:    stmt.ColumnText(2),
			Bio:            stmt.ColumnText(3),
			AvatarUploadID: stmt.ColumnInt64(4),
		}
		return nil
	}
	err := sqlitex.Exec(conn, query, collect, name)
	return user, err
}

const MaxPostLength = 256

func CreatePost(conn *sqlite.Conn, userID int64, content string) (int64, error) {
	// Being kinda lame and just truncating when the content is too long. We have a
	// maxlength on the client side to enforce it there.
	if len(content) > MaxPostLength {
		content = content[:MaxPostLength]
	}
	query := "insert into post (user_id, created_at, content) values (?, ?, ?)"
	err := sqlitex.Exec(conn, query, nil, userID, utcNow().Unix(), content)
	if err != nil {
		return 0, err
	}
	postID := conn.LastInsertRowID()
	return postID, err
}

func ReplyToPost(conn *sqlite.Conn, postID int64, userID int64, content string) (int64, error) {
	var err error
	defer sqlitex.Save(conn)(&err)
	postReplyID, err := CreatePost(conn, userID, content)
	if err != nil {
		return 0, err
	}
	query := "insert into post_reply (post_id, reply_post_id) values (?, ?)"
	if err = sqlitex.Exec(conn, query, nil, postID, postReplyID); err != nil {
		return 0, err
	}
	return postReplyID, err
}

func ReactToPostIfExists(conn *sqlite.Conn, userID int64, postID int64, emoji string) (bool, error) {
	query := "select 1 from post where post_id = ?"
	exists := false
	collect := func(stmt *sqlite.Stmt) error {
		exists = true
		return nil
	}
	if err := sqlitex.Exec(conn, query, collect, postID); err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}
	query = `
		insert into reaction (post_id, user_id, reacted_at, emoji)
		values (:postID, :userID, :reactedAt, :emoji)
		on conflict do nothing`
	err := exec(conn, query, nil, func(stmt *sqlite.Stmt) error {
		stmt.SetInt64(":postID", postID)
		stmt.SetInt64(":userID", userID)
		stmt.SetInt64(":reactedAt", utcNow().Unix())
		stmt.SetText(":emoji", emoji)
		return nil
	})
	if err != nil {
		return exists, err
	}
	return exists, err
}

func UnreactToPostIfExists(conn *sqlite.Conn, userID int64, postID int64) (bool, error) {
	// Do we even care if it exists?
	query := "select 1 from post where post_id = ?"
	exists := false
	collect := func(stmt *sqlite.Stmt) error {
		exists = true
		return nil
	}
	if err := sqlitex.Exec(conn, query, collect, postID); err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}
	query = "delete from reaction where post_id = :postID and user_id = :userID"
	err := exec(conn, query, nil, func(stmt *sqlite.Stmt) error {
		stmt.SetInt64(":postID", postID)
		stmt.SetInt64(":userID", userID)
		return nil
	})
	if err != nil {
		return exists, err
	}
	return exists, err
}

// Sets the Reactions field on each post in the given slice.
func getReactionCountsForPosts(conn *sqlite.Conn, user *User, posts []Post) error {
	var userID int64
	if user != nil {
		userID = user.UserID
	}
	query := `
		select
			post_id,
			emoji,
			count(*) as count,
			case
				when :userID = 0 then 0
				else sum(case when user_id = :userID then 1 else 0 end)
			end as user_reacted
		from reaction
		where post_id in (select value from json_each(:postIDsJSON))
		group by post_id, emoji
		`
	postIDs := make([]int64, len(posts))
	postsByID := make(map[int64]*Post)
	for i := range posts {
		postsByID[posts[i].PostID] = &posts[i]
		postIDs[i] = posts[i].PostID
	}
	postIDsJSON, err := json.Marshal(postIDs)
	if err != nil {
		return err
	}
	collect := func(stmt *sqlite.Stmt) error {
		postID := stmt.ColumnInt64(0)
		postsByID[postID].Reactions = append(postsByID[postID].Reactions, PostReactionCount{
			Emoji:       stmt.ColumnText(1),
			Count:       stmt.ColumnInt(2),
			UserReacted: stmt.ColumnInt(3) > 0,
		})
		return nil
	}
	return exec(conn, query, collect, func(stmt *sqlite.Stmt) error {
		stmt.SetText(":postIDsJSON", string(postIDsJSON))
		stmt.SetInt64(":userID", userID)
		return nil
	})
}

func getParentsForPosts(conn *sqlite.Conn, posts []Post) error {
	query := `
		select
			post_reply.reply_post_id,
			post_reply.post_id,
			user.user_name
		from post_reply
		join post using (post_id)
		join user using (user_id)
		where reply_post_id in (select value from json_each(:postIDsJSON))
		`
	postIDs := make([]int64, len(posts))
	postsByID := make(map[int64]*Post)
	for i := range posts {
		postsByID[posts[i].PostID] = &posts[i]
		postIDs[i] = posts[i].PostID
	}
	postIDsJSON, err := json.Marshal(postIDs)
	if err != nil {
		return err
	}
	collect := func(stmt *sqlite.Stmt) error {
		replyPostID := stmt.ColumnInt64(0)
		originalPostID := stmt.ColumnInt64(1)
		originalPostUserName := stmt.ColumnText(2)
		postsByID[replyPostID].ReplyingToPostID = originalPostID
		postsByID[replyPostID].ReplyingToPostUserName = originalPostUserName
		return nil
	}
	return exec(conn, query, collect, func(stmt *sqlite.Stmt) error {
		stmt.SetText(":postIDsJSON", string(postIDsJSON))
		return nil
	})
}

func getReplyCountsForPosts(conn *sqlite.Conn, posts []Post) error {
	query := `
		select
			post_reply.post_id,
			count(*) as reply_count
		from post_reply
		join post using (post_id)
		where post_reply.post_id in (select value from json_each(:postIDsJSON))
		group by 1
		`
	postIDs := make([]int64, len(posts))
	postsByID := make(map[int64]*Post)
	for i := range posts {
		postsByID[posts[i].PostID] = &posts[i]
		postIDs[i] = posts[i].PostID
	}
	postIDsJSON, err := json.Marshal(postIDs)
	if err != nil {
		return err
	}
	collect := func(stmt *sqlite.Stmt) error {
		postID := stmt.ColumnInt64(0)
		replyCount := stmt.ColumnInt64(1)
		postsByID[postID].ReplyCount = int(replyCount)
		return nil
	}
	return exec(conn, query, collect, func(stmt *sqlite.Stmt) error {
		stmt.SetText(":postIDsJSON", string(postIDsJSON))
		return nil
	})
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

func UpdateUserProfile(conn *sqlite.Conn, name string, displayName string, bio string, avatarUploadID int64) error {
	query := `
		update user
		set
			display_name = :displayName,
			bio = :bio,
			avatar_upload_id = case when :upload_id != 0 then :upload_id else avatar_upload_id end
		where user_name = :name`
	return exec(conn, query, nil, func(stmt *sqlite.Stmt) error {
		stmt.SetText(":displayName", displayName)
		stmt.SetText(":bio", bio)
		stmt.SetText(":name", name)
		stmt.SetInt64(":upload_id", avatarUploadID)
		return nil
	})
}

func CreateUserSession(conn *sqlite.Conn, userID int64) (*UserSession, error) {
	sessionPublicID := make([]byte, 8)
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
		select user_id, user.user_name, user.display_name, user.bio, user.avatar_upload_id
		from user_session
		join user using (user_id)
		where session_public_id = ? and expiration_time > ?`
	var user *User
	collect := func(stmt *sqlite.Stmt) error {
		user = &User{
			UserID:         stmt.ColumnInt64(0),
			Name:           stmt.ColumnText(1),
			DisplayName:    stmt.ColumnText(2),
			Bio:            stmt.ColumnText(3),
			AvatarUploadID: stmt.ColumnInt64(4),
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
func distortPostsForUser(conn *sqlite.Conn, user *User, posts []Post) error {
	if user == nil {
		for i := range posts {
			posts[i].Content = DistortContent(posts[i].Content, MaxDistortionLevel)
			posts[i].DistanceFromUser = MaxDistortionLevel
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
		posts[i].DistanceFromUser = distances[posts[i].UserID]
	}
	return nil
}

// Decorate posts with the usual extra metadata, and distort them based on the distance
// between the given user and the post's author.
func DecoratePosts(conn *sqlite.Conn, user *User, posts []Post) error {
	if len(posts) == 0 {
		return nil
	}
	if err := getReactionCountsForPosts(conn, user, posts); err != nil {
		return err
	}
	if err := getReplyCountsForPosts(conn, posts); err != nil {
		return err
	}
	if err := distortPostsForUser(conn, user, posts); err != nil {
		return err
	}
	if err := getParentsForPosts(conn, posts); err != nil {
		return err
	}
	return nil
}

func randomHex() (string, error) {
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		// Should probably just panic, tbh
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func SaveUpload(conn *sqlite.Conn, contentType string, contents []byte) (int64, error) {
	stem, err := randomHex()
	if err != nil {
		return 0, err
	}
	exts, err := mime.ExtensionsByType(contentType)
	if err != nil {
		return 0, err
	}
	filename := stem + exts[0]
	query := "insert into upload (filename, created_at, content_type, contents) values (?, ?, ?, ?)"
	err = sqlitex.Exec(conn, query, nil, filename, utcNow().Unix(), contentType, contents)
	if err != nil {
		return 0, err
	}
	return conn.LastInsertRowID(), err
}

func OpenUploadContents(conn *sqlite.Conn, uploadID int64) (blob io.ReadCloser, contentType string, err error) {
	query := "select content_type from upload where upload_id = ? limit 1"
	collect := func(stmt *sqlite.Stmt) error {
		contentType = stmt.ColumnText(0)
		return nil
	}
	if err := sqlitex.Exec(conn, query, collect, uploadID); err != nil {
		return nil, "", err
	}
	blob, err = conn.OpenBlob("", "upload", "contents", uploadID, false)
	return blob, contentType, err
}
