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
		args := parseArgs(strings.TrimSpace(command))
		if len(args) == 0 {
			continue
		}
		switch args[0] {
		case "exit":
			return
		case "echo":
			fmt.Println(strings.Join(args[1:], " "))
		case "type":
			for _, name := range args[1:] {
				if builtInCommands[name] {
					fmt.Println(name + " is a shell builtin")
				} else if path, err := exec.LookPath(name); err == nil {
					fmt.Println(name + " is " + path)
				} else {
					fmt.Println(name + ": not found")
				}
			}
		case "pwd":
			if dir, err := os.Getwd(); err == nil {
				fmt.Println(dir)
			} else {
				_, _ = fmt.Fprintln(os.Stderr, "pwd:", err)
			}
		case "cd":
			dir := "~"
			if len(args) > 1 {
				dir = args[1]
			}
			if dir == "~" || strings.HasPrefix(dir, "~/") {
				dir = os.Getenv("HOME") + dir[1:]
			}
			if err := os.Chdir(dir); errors.Is(err, os.ErrNotExist) {
				_, _ = fmt.Fprintln(os.Stderr, "cd: "+dir+": No such file or directory")
			} else if err != nil {
				_, _ = fmt.Fprintln(os.Stderr, "cd:", err)
			}
		default:
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
}

// parseArgs splits line into arguments at unquoted spaces and tabs. Text inside
// single quotes is taken literally, and adjacent pieces join into one argument.
func parseArgs(line string) []string {
	var args []string
	var arg strings.Builder
	inArg := false // true once an argument has started, even an empty one like ''
	inQuotes := false
	for _, r := range line {
		switch {
		case inQuotes && r == '\'':
			inQuotes = false
		case inQuotes:
			arg.WriteRune(r)
		case r == '\'':
			inQuotes, inArg = true, true
		case r == ' ' || r == '\t':
			if inArg {
				args = append(args, arg.String())
				arg.Reset()
				inArg = false
			}
		default:
			arg.WriteRune(r)
			inArg = true
		}
	}
	if inArg {
		args = append(args, arg.String())
	}
	return args
}
