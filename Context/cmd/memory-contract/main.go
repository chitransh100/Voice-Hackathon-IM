package main

import (
	"fmt"

	"vani-context/memorycontract"
)

// This command prints the exact extraction prompt compiled into the shared
// memory contract. Use its output in Sarvam instead of maintaining a second
// hand-edited prompt.
func main() {
	fmt.Println(memorycontract.ExtractionPrompt)
}
