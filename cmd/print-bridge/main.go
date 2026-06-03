package main

import (
	"fmt"

	"github.com/robsonek/print-bridge/internal/version"
)

func main() {
	fmt.Printf("print-bridge %s\n", version.Version)
}
