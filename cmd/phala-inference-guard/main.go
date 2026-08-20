package main

import (
	"log"
	"os"

	"github.com/Phala-Network/phala-inference-guard/internal/app/server"
)

func main() {
	if err := server.Run(); err != nil {
		log.Printf("level=error component=runtime event=exit error=%q", err)
		os.Exit(1)
	}
}
