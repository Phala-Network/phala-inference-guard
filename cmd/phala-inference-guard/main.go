package main

import (
	"log"

	"github.com/Phala-Network/phala-inference-guard/internal/app/server"
)

func main() {
	log.Fatal(server.Run())
}
