package main

import (
	"bufio"
	"fmt"
	"os"
)

func main() {
	// Wait for user input
	command, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "Error reading input:", err)
		os.Exit(1)
	}
	fmt.Println(command[:len(command)-1] + ": command not found")
}
