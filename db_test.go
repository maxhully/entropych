package entropy

import (
	"context"
	"testing"

	"crawshaw.io/sqlite/sqlitex"
)

func setUpTestDB(t *testing.T) *DB {
	dbpool, err := sqlitex.Open("file::memory:?mode=memory", 0, 1)
	if err != nil {
		t.Fatalf("could not open pool: %v", err)
	}
	db, err := NewDB(dbpool)
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
