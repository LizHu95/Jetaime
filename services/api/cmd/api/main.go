package main

import (
	"log"
	"os"

	"github.com/LizHu95/Jetaime/services/api/internal/app"
)

func main() {
	if err := app.Run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}
