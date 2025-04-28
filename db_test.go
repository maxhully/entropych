package entropy

import (
	"context"
	"io"
	"path"
	"testing"

	"github.com/stretchr/testify/assert"
)

func setUpTestDB(t *testing.T) *DB {
	dir := t.TempDir()
	uri := path.Join(dir, "temptest.db")
	db, err := NewDB(uri, 10)
	if err != nil {
		t.Fatalf("NewDB error: %v", err)
	}
	return db
}

func TestFollowUser(t *testing.T) {
	db := setUpTestDB(t)
	defer db.Close()

	conn := db.Get(context.TODO())
	defer db.Put(conn)

	maxUser, err := CreateUser(conn, "Max", "maxpass")
	assert.Nil(t, err)
	lunaUser, err := CreateUser(conn, "Luna", "lunapass")
	assert.Nil(t, err)
	err = FollowUser(conn, maxUser.UserID, lunaUser.UserID)
	assert.Nil(t, err)
	err = FollowUser(conn, maxUser.UserID, lunaUser.UserID)
	assert.Nil(t, err)

	dists, err := GetDistanceFromUser(conn, maxUser.UserID, []int64{lunaUser.UserID})
	assert.Nil(t, err)
	assert.Equal(t, dists[lunaUser.UserID], 1)

	err = FollowUser(conn, maxUser.UserID, maxUser.UserID)
	assert.NotNil(t, err)
}

func TestFollowerStats(t *testing.T) {
	db := setUpTestDB(t)
	defer db.Close()

	conn := db.Get(context.TODO())
	defer db.Put(conn)

	maxUser, err := CreateUser(conn, "Max", "maxpass")
	assert.Nil(t, err)
	lunaUser, err := CreateUser(conn, "Luna", "lunapass")
	assert.Nil(t, err)
	err = FollowUser(conn, maxUser.UserID, lunaUser.UserID)
	assert.Nil(t, err)

	followerStats, err := GetUserFollowStats(conn, maxUser.UserID)
	assert.Nil(t, err)
	assert.EqualValues(t, followerStats.FollowerCount, 0)
	assert.EqualValues(t, followerStats.FollowingCount, 1)

	followerStats, err = GetUserFollowStats(conn, lunaUser.UserID)
	assert.Nil(t, err)
	assert.EqualValues(t, followerStats.FollowerCount, 1)
	assert.EqualValues(t, followerStats.FollowingCount, 0)
}

func TestGetDistanceFromUser(t *testing.T) {
	db := setUpTestDB(t)
	defer db.Close()

	conn := db.Get(context.TODO())
	defer db.Put(conn)

	maxUser, err := CreateUser(conn, "Max", "maxpass")
	assert.Nil(t, err)
	lunaUser, err := CreateUser(conn, "Luna", "lunapass")
	assert.Nil(t, err)
	err = FollowUser(conn, maxUser.UserID, lunaUser.UserID)
	assert.Nil(t, err)
	birdUser, err := CreateUser(conn, "Bird", "birdpass")
	assert.Nil(t, err)
	err = FollowUser(conn, lunaUser.UserID, birdUser.UserID)
	assert.Nil(t, err)
	strangerUser, err := CreateUser(conn, "Stranger", "strangerpass")
	assert.Nil(t, err)

	dists, err := GetDistanceFromUser(conn, maxUser.UserID, []int64{lunaUser.UserID, birdUser.UserID, strangerUser.UserID})
	assert.Nil(t, err)
	assert.Equal(t, dists[lunaUser.UserID], 1)
	assert.Equal(t, dists[birdUser.UserID], 2)
	assert.Equal(t, dists[strangerUser.UserID], MaxDistortionLevel)
}

func TestUploads(t *testing.T) {
	db := setUpTestDB(t)
	defer db.Close()

	conn := db.Get(context.TODO())
	defer db.Put(conn)

	uploadID, err := saveUpload(conn, "hello.txt", "text/plain", []byte("hello, world!"))
	assert.Nil(t, err)
	assert.Greater(t, uploadID, int64(0))

	blob, contentType, err := OpenUploadContents(conn, uploadID)
	assert.Nil(t, err)
	assert.Equal(t, contentType, "text/plain")
	contents, err := io.ReadAll(blob)
	assert.Nil(t, err)
	assert.Equal(t, string(contents), "hello, world!")
}
