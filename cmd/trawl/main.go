package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: trawl <package-pattern>\n")
		os.Exit(1)
	}
	fmt.Println("trawl: not yet implemented")
}
