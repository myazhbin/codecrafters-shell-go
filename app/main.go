package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

var builtInCommands = map[string]bool{
	"exit": true,
	"echo": true,
	"type": true,
}

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
		if command == "" {
			continue
		}
		if command == "exit" {
			break
		}
		if strings.HasPrefix(command, "echo ") {
			fmt.Println(command[5:])
			continue
		}
		if strings.HasPrefix(command, "type ") {
			name := command[5:]
			if builtInCommands[name] {
				fmt.Println(name + " is a shell builtin")
			} else if path, err := exec.LookPath(name); err == nil {
				fmt.Println(name + " is " + path)
			} else {
				fmt.Println(name + ": not found")
			}
			continue
		}
		args := strings.Fields(command)
		if _, err := exec.LookPath(args[0]); err != nil {
			fmt.Println(args[0] + ": command not found")
			continue
		}
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		_ = cmd.Run()
	}
}
