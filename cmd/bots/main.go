// bots: posts to entropy.social using bot accounts (writing directly to the SQLite
// database).
//
// Each bot user is a character from a shakespeare play. The characters post their lines
// from the play. If two characters share the stage, they follow each other on the site.
//
// I'm using this repo as the source of the data, since they (very nicely!) have all the
// plays in CSV form:
// https://github.com/nrennie/shakespeare

package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"strconv"

	"crawshaw.io/sqlite"
	"crawshaw.io/sqlite/sqlitex"
	"github.com/maxhully/entropy"
)

// A line of dialogue, as represented in the nrennie/shakespeare repo
type dialogueLine struct {
	act       string
	scene     string
	character string
	dialogue  string
	lineNum   int // zero if the cell in the CSV is "NA"
}

// Reads and parses CSV lines of Shakespeare dialogue.
//
// Returns two channels---one for the parsed lines, one for the errors.
func streamDialogueLines(reader io.Reader) (<-chan dialogueLine, <-chan error) {
	lines := make(chan dialogueLine, 12)
	errChan := make(chan error, 1)
	go func() {
		defer close(lines)
		defer close(errChan)

		csvReader := csv.NewReader(reader)
		csvReader.FieldsPerRecord = 5

		sawHeader := false
		for {
			rawLine, err := csvReader.Read()
			if err == io.EOF {
				return
			}
			if err != nil {
				errChan <- err
				return
			}

			if !sawHeader && rawLine[0] == "act" {
				sawHeader = true
				continue
			}

			lineNum := 0
			if rawLine[4] != "NA" {
				lineNum, err = strconv.Atoi(rawLine[4])
				// Probably should just ignore these errors, actually
				if err != nil {
					return
				}
			}

			line := dialogueLine{
				act:       rawLine[0],
				scene:     rawLine[1],
				character: rawLine[2],
				dialogue:  rawLine[3],
				lineNum:   lineNum,
			}
			lines <- line
		}
	}()

	return lines, errChan
}

const csvURL = "https://raw.githubusercontent.com/nrennie/shakespeare/refs/heads/main/data/twelfth_night.csv"

func getOrCreateUser(conn *sqlite.Conn, name string) (*entropy.User, error) {
	user, err := entropy.GetUserByName(conn, name)
	if err != nil {
		return nil, err
	}
	if user == nil {
		// Use name as password for these bots
		user, err = entropy.CreateUser(conn, name, name)
		if err != nil {
			return nil, err
		}
	}
	return user, err
}

func main() {
	resp, err := http.Get(csvURL)
	if err != nil {
		log.Fatalf("GET %s failed: %v", csvURL, err)
	}
	defer resp.Body.Close()
	lines, errChan := streamDialogueLines(resp.Body)

	dbpool, err := sqlitex.Open("test.db", 0, 10)
	if err != nil {
		log.Fatal(err)
	}
	defer dbpool.Close()
	db, err := entropy.NewDB(dbpool)
	if err != nil {
		log.Fatal(err)
	}
	conn := db.Get(context.Background())
	defer db.Put(conn)

	linesByCharacter := make(map[string]int)
	charactersInScene := make(map[int64]bool)
	var currentScene []string // (act, scene) pair

	// We close both channels when an error happens, so we can safely range over these
	// channels to get all the lines we parsed and all the errors (one or zero) that we
	// hit.
	for line := range lines {
		fmt.Printf("line: %v\n", line)

		scene := []string{line.act, line.scene}
		if currentScene == nil || !slices.Equal(scene, currentScene) {
			clear(charactersInScene)
			currentScene = scene
		}

		linesByCharacter[line.character]++
		user, err := getOrCreateUser(conn, line.character)
		if err != nil {
			log.Fatalf("could not get or create user: %v", err)
		}
		charactersInScene[user.UserID] = true

		// entropy.CreatePost(conn, user.UserID, line.dialogue)

		// Have both users follow each other
		for otherUserID := range charactersInScene {
			entropy.FollowUser(conn, user.UserID, otherUserID)
			entropy.FollowUser(conn, otherUserID, user.UserID)
		}
	}

	for err := range errChan {
		fmt.Printf("error: %v\n", err)
	}

	for k, v := range linesByCharacter {
		fmt.Printf("%s: %d\n", k, v)
	}
}
