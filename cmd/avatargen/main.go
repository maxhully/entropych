package main

import (
	"fmt"
	"image/png"
	"log"
	"os"

	"github.com/maxhully/entropy/avatargen"
)

func main() {
	image, err := avatargen.GenerateAvatar()
	if err != nil {
		log.Fatal(err)
	}
	f, _ := os.Create("avatargen_out.png")
	png.Encode(f, image)
	fmt.Println("Created avatargen_out.png")
}
