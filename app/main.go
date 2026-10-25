package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

func main() {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("$ ")
		command, err := reader.ReadString('\n')
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "Error reading input:", err)
			os.Exit(1)
		}
		command = strings.TrimSpace(command)
		if command == "exit" {
			break
		}
		if strings.HasPrefix(command, "echo ") {
			fmt.Println(command[5:])
			continue
		}
		if strings.HasPrefix(command, "type ") {
			name := command[5:]
			switch name {
			case "echo", "exit", "type":
				fmt.Println(name + " is a shell builtin")
			default:
				fmt.Println(name + ": not found")
			}
			continue
		}
		fmt.Println(command + ": command not found")
	}
}
