// bots: posts to entropych.social using bot accounts (writing directly to the SQLite
// database).
//
// Each bot user is a character from a shakespeare play. The characters post their lines
// from the play. If two characters share the stage, they follow each other on the site.
//
// I'm using this repo as the source of the data, since they (very nicely!) have all the
// plays in CSV form: https://github.com/nrennie/shakespeare

package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"log"

	"crawshaw.io/sqlite"
	"crawshaw.io/sqlite/sqlitex"
	"github.com/maxhully/entropy"
	"github.com/maxhully/entropy/avatargen"
)

func main() {
	var dbFilename string
	flag.StringVar(&dbFilename, "db", "test.db", "Filename of the SQLite database to connect to")
	flag.Parse()

	db, err := entropy.NewDB(dbFilename, 10)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	conn := db.Get(context.Background())
	defer db.Put(conn)
	err = backfillEmptyAvatars(conn)
	if err != nil {
		log.Fatal(err)
	}
}

func backfillEmptyAvatars(conn *sqlite.Conn) error {
	query := `
	select user_id, user.user_name, user.display_name, user.bio, user.avatar_upload_id
	from user
	where user.avatar_upload_id is null`
	users := make([]entropy.User, 0, 0)
	collect := func(stmt *sqlite.Stmt) error {
		users = append(users, entropy.User{
			UserID:         stmt.ColumnInt64(0),
			Name:           stmt.ColumnText(1),
			DisplayName:    stmt.ColumnText(2),
			Bio:            stmt.ColumnText(3),
			AvatarUploadID: stmt.ColumnInt64(4),
		})
		return nil
	}
	if err := sqlitex.Exec(conn, query, collect); err != nil {
		return err
	}
	var err error
	sqlitex.Save(conn)(&err)
	buf := new(bytes.Buffer)
	for i := range users {
		fmt.Printf("backfilling avatar for %s\n", users[i].Name)
		if err := avatargen.GenerateAvatarPNG(buf); err != nil {
			return err
		}
		uploadID, err := entropy.SaveUpload(conn, "image/png", buf.Bytes())
		if err != nil {
			return err
		}
		buf.Reset()
		err = entropy.UpdateUserProfile(conn, users[i].Name, users[i].DisplayName, users[i].Bio, uploadID)
		if err != nil {
			return err
		}
	}
	return nil
}
