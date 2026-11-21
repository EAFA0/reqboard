package main

import (
	"os"

	"reqboard/internal/cmd"
)

func main() {
	os.Exit(cmd.Main(os.Args[1:]))
}
