package entropy

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
)

func setUpTestDB(t *testing.T) *DB {
	db, err := NewDB("file::memory:?mode=memory", 1)
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
	if err != nil {
		t.Fatalf("could not create user Max: %v", err)
	}
	lunaUser, err := CreateUser(conn, "Luna", "lunapass")
	if err != nil {
		t.Fatalf("could not create user Max: %v", err)
	}
	err = FollowUser(conn, maxUser.UserID, lunaUser.UserID)
	if err != nil {
		t.Fatalf("could not follow user: %v", err)
	}
	err = FollowUser(conn, maxUser.UserID, lunaUser.UserID)
	if err != nil {
		t.Fatalf("FollowUser not idempotent: %v", err)
	}

	isFollowing, err := IsFollowing(conn, maxUser.UserID, lunaUser.UserID)
	if err != nil {
		t.Fatalf("IsFollowing: %v", err)
	}
	if !isFollowing {
		t.Error("expected isFollowing to be true")
	}

	err = FollowUser(conn, maxUser.UserID, maxUser.UserID)
	if err == nil {
		t.Fatal("expected error when following self")
	}
}

func TestFollowerStats(t *testing.T) {
	db := setUpTestDB(t)
	defer db.Close()

	conn := db.Get(context.TODO())
	defer db.Put(conn)

	maxUser, err := CreateUser(conn, "Max", "maxpass")
	if err != nil {
		t.Fatalf("could not create user Max: %v", err)
	}
	lunaUser, err := CreateUser(conn, "Luna", "lunapass")
	if err != nil {
		t.Fatalf("could not create user Max: %v", err)
	}
	err = FollowUser(conn, maxUser.UserID, lunaUser.UserID)
	if err != nil {
		t.Fatalf("could not follow user: %v", err)
	}

	followerStats, err := GetUserFollowStats(conn, maxUser.UserID)
	if err != nil {
		t.Fatalf("could not get follower stats: %v", err)
	}
	if followerStats.FollowerCount != 0 {
		t.Fatalf("followerStats.FollowerCount: %v, expected 0\n", followerStats.FollowerCount)
	}
	if followerStats.FollowingCount != 1 {
		t.Fatalf("followerStats.FollowerCount: %v, expected 1\n", followerStats.FollowerCount)
	}

	followerStats, err = GetUserFollowStats(conn, lunaUser.UserID)
	if err != nil {
		t.Fatalf("could not get follower stats: %v", err)
	}
	if followerStats.FollowerCount != 1 {
		t.Fatalf("followerStats.FollowerCount: %v, expected 1\n", followerStats.FollowerCount)
	}
	if followerStats.FollowingCount != 0 {
		t.Fatalf("followerStats.FollowerCount: %v, expected 0\n", followerStats.FollowerCount)
	}
}

func TestGetDistanceFromUser(t *testing.T) {
	db := setUpTestDB(t)
	defer db.Close()

	conn := db.Get(context.TODO())
	defer db.Put(conn)

	maxUser, err := CreateUser(conn, "Max", "maxpass")
	if err != nil {
		t.Fatalf("could not create user Max: %v", err)
	}
	lunaUser, err := CreateUser(conn, "Luna", "lunapass")
	if err != nil {
		t.Fatalf("could not create user Luna: %v", err)
	}
	err = FollowUser(conn, maxUser.UserID, lunaUser.UserID)
	if err != nil {
		t.Fatalf("could not follow user: %v", err)
	}
	birdUser, err := CreateUser(conn, "Bird", "birdpass")
	if err != nil {
		t.Fatalf("could not create user Bird: %v", err)
	}
	err = FollowUser(conn, lunaUser.UserID, birdUser.UserID)
	if err != nil {
		t.Fatalf("could not follow user: %v", err)
	}
	strangerUser, err := CreateUser(conn, "Stranger", "strangerpass")
	if err != nil {
		t.Fatalf("could not create user Stranger: %v", err)
	}

	dists, err := GetDistanceFromUser(conn, maxUser.UserID, []int64{lunaUser.UserID, birdUser.UserID, strangerUser.UserID})
	if err != nil {
		t.Fatalf("could not get distances: %v", err)
	}
	if dists[lunaUser.UserID] != 1 {
		t.Errorf("expected dists[%v]=%v to be 1", lunaUser.UserID, dists[lunaUser.UserID])
	}
	if dists[birdUser.UserID] != 2 {
		t.Errorf("expected dists[%v]=%v to be 2", birdUser.UserID, dists[birdUser.UserID])
	}
	if dists[strangerUser.UserID] != MaxDistortionLevel {
		t.Errorf("expected dists[%v]=%v to be %d", strangerUser.UserID, dists[strangerUser.UserID], MaxDistortionLevel)
	}
}

func TestUploads(t *testing.T) {
	db := setUpTestDB(t)
	defer db.Close()

	conn := db.Get(context.TODO())
	defer db.Put(conn)

	uploadID, err := SaveUpload(conn, "hello.txt", "text/plain", []byte("hello, world!"))
	assert.Nil(t, err)
	assert.Greater(t, uploadID, int64(0))

	blob, contentType, err := OpenUploadContents(conn, uploadID)
	assert.Nil(t, err)
	assert.Equal(t, contentType, "text/plain")
	contents, err := io.ReadAll(blob)
	assert.Nil(t, err)
	assert.Equal(t, string(contents), "hello, world!")
}
