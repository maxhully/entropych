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
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"slices"
	"strconv"
	"time"

	"crawshaw.io/sqlite"
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
func streamDialogueLines(reader io.Reader, fromLine int) (<-chan dialogueLine, <-chan error) {
	lines := make(chan dialogueLine, 128)
	errChan := make(chan error, 1)
	foundStartLine := false
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
			if !foundStartLine {
				if lineNum >= fromLine {
					foundStartLine = true
				} else {
					continue
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

const csvURLPrefix = "https://raw.githubusercontent.com/nrennie/shakespeare/refs/heads/main/data/"

func main() {
	var shouldPost bool
	var playCSVName string
	var dbFilename string
	var fromLine int
	var maxSleep int

	flag.StringVar(&dbFilename, "db", "test.db", "Filename of the SQLite database to connect to")
	flag.BoolVar(&shouldPost, "posts", false, "Create Posts using the dialogue lines (not idempotent!)")
	flag.IntVar(&maxSleep, "sleep", 10, "Max. random sleep between posts (to imitate humans)")
	flag.StringVar(&playCSVName, "play", "", "Filename of the CSV in the nrennie/shakespeare repo (e.g. 'twelfth_night.csv')")
	flag.IntVar(&fromLine, "from-line", 0, "Start processing lines this line_number")

	flag.Parse()

	if playCSVName == "" {
		log.Fatalf("--play is required")
	}
	csvURL := csvURLPrefix + playCSVName

	resp, err := http.Get(csvURL)
	if err != nil {
		log.Fatalf("GET %s failed: %v", csvURL, err)
	}
	if resp.StatusCode != http.StatusOK {
		log.Fatalf("GET %s returned %s", csvURL, resp.Status)
	}
	defer resp.Body.Close()
	lines, errChan := streamDialogueLines(resp.Body, fromLine)

	db, err := entropy.NewDB(dbFilename, 10)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	conn := db.Get(context.Background())
	defer db.Put(conn)

	linesByCharacter := make(map[string]int)
	charactersInScene := make(map[int64]bool)
	var currentScene []string // (act, scene) pair

	// We close both channels when an error happens, so we can safely range over these
	// channels to get all the lines we parsed and all the errors (one or zero) that we
	// hit.
	var lineToPost dialogueLine
	for line := range lines {
		// Random pause
		if maxSleep > 0 {
			time.Sleep(time.Second * time.Duration(float32(maxSleep)*rand.Float32()))
		}

		fmt.Printf("line: %v\n", line)

		scene := []string{line.act, line.scene}
		if currentScene == nil || !slices.Equal(scene, currentScene) {
			clear(charactersInScene)
			currentScene = scene
		}

		linesByCharacter[line.character]++
		user, err := getOrCreateUser(conn, line.character)
		if err != nil {
			log.Fatalf("could not get or create user %v: %v", line.character, err)
		}
		charactersInScene[user.UserID] = true

		// Have both users follow each other
		for otherUserID := range charactersInScene {
			entropy.FollowUser(conn, user.UserID, otherUserID)
			entropy.FollowUser(conn, otherUserID, user.UserID)
		}

		if !shouldPost {
			continue
		}
		// If this is the first iteration, lineToPost is empty so we set it and continue
		if lineToPost.dialogue == "" {
			lineToPost = line
			continue
		}
		// Sorry for this formatting
		if len(lineToPost.dialogue)+len(line.dialogue) < 256 &&
			lineToPost.act == line.act &&
			lineToPost.scene == line.scene &&
			lineToPost.character == line.character {
			lineToPost.dialogue += " " + line.dialogue
		} else {
			// Post the accumulated line, because line is not a continuation of it (or the accumulated line has gotten too long)
			postDialogueLine(conn, &lineToPost)
			lineToPost = line
		}
	}
	if lineToPost.dialogue != "" {
		postDialogueLine(conn, &lineToPost)
	}

	for err := range errChan {
		fmt.Printf("error: %v\n", err)
	}

	for k, v := range linesByCharacter {
		fmt.Printf("%s: %d\n", k, v)
	}
}

func postDialogueLine(conn *sqlite.Conn, line *dialogueLine) {
	user, err := getOrCreateUser(conn, line.character)
	if err != nil {
		log.Fatalf("could not get or create user %v: %v", line.character, err)
	}
	_, err = entropy.CreatePost(conn, user.UserID, line.dialogue)
	if err != nil {
		log.Fatalf("could not post: %v", err)
	}
	fmt.Printf("[%v]: %v", line.character, line.dialogue)
}
