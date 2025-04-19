package entropy

import (
	mathrand "math/rand"
	"sort"
	"time"

	"crawshaw.io/sqlite"
)

// Get recommended posts, based on the ENTROPYCH, INC. CHAOS RECOMMENDATION ALGORITHM
func GetRecommendedPosts(conn *sqlite.Conn, user *User, before time.Time, limit int) ([]Post, error) {
	if user == nil {
		return GetRecentPosts(conn, before, limit)
	}
	var posts []Post
	followedPosts, err := GetRecentPostsFromFollowedUsers(conn, user.UserID, before, limit)
	if err != nil {
		return nil, err
	}
	chaosPosts, err := GetRecentPostsFromRandos(conn, user.UserID, before, limit)
	if err != nil {
		return nil, err
	}
	posts = make([]Post, 0, limit)
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
	err = DistortPostsForUser(conn, user, posts)
	if err != nil {
		return nil, err
	}
	return posts, err
}
