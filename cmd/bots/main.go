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
	"encoding/csv"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
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

func main() {
	resp, err := http.Get(csvURL)
	if err != nil {
		log.Fatalf("GET %s failed: %v", csvURL, err)
	}
	defer resp.Body.Close()

	linesByCharacter := make(map[string]int)

	lines, errChan := streamDialogueLines(resp.Body)

	// We close both channels at the first sign of an error, so we can safely range over
	// these channels to get all the lines we parsed and all the errors (one or zero)
	// that we hit.
	for line := range lines {
		fmt.Printf("line: %v\n", line)
		linesByCharacter[line.character]++
	}
	for err := range errChan {
		fmt.Printf("error: %v\n", err)
	}

	for k, v := range linesByCharacter {
		fmt.Printf("%s: %d\n", k, v)
	}
}
