package main

import (
	"fmt"
	"log"
	"os"

	"github.com/maxhully/entropy/avatargen"
)

func main() {
	f, _ := os.Create("avatargen_out.png")
	defer f.Close()
	if err := avatargen.GenerateAvatarPNG(f); err != nil {
		log.Fatal(err)
	}
	fmt.Println("Created avatargen_out.png")
}
