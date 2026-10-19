package main

import (
	"log"
	"os"

	"github.com/LizHu95/Jetaime/services/api/internal/app"
)

func main() {
	args := append([]string{"-auth=false", "-assistant"}, os.Args[1:]...)
	if err := app.Run(args); err != nil {
		log.Fatal(err)
	}
}
