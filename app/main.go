package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

var builtInCommands = map[string]bool{
	"exit": true,
	"echo": true,
	"type": true,
	"pwd":  true,
	"cd":   true,
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
		if command == "pwd" {
			if dir, err := os.Getwd(); err == nil {
				fmt.Println(dir)
			} else {
				_, _ = fmt.Fprintln(os.Stderr, "pwd:", err)
			}
			continue
		}
		if strings.HasPrefix(command, "cd ") {
			dir := command[3:]
			if dir == "~" || strings.HasPrefix(dir, "~/") {
				dir = os.Getenv("HOME") + dir[1:]
			}
			if err := os.Chdir(dir); errors.Is(err, os.ErrNotExist) {
				_, _ = fmt.Fprintln(os.Stderr, "cd: "+dir+": No such file or directory")
			} else if err != nil {
				_, _ = fmt.Fprintln(os.Stderr, "cd:", err)
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
