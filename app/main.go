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
		args, stdoutFile := parseCommand(strings.TrimRight(command, "\r\n"))
		if len(args) == 0 {
			continue
		}
		stdout := os.Stdout
		if stdoutFile != "" {
			file, err := os.Create(stdoutFile)
			if err != nil {
				_, _ = fmt.Fprintln(os.Stderr, err)
				continue
			}
			stdout = file
		}
		switch args[0] {
		case "exit":
			return
		case "echo":
			_, _ = fmt.Fprintln(stdout, strings.Join(args[1:], " "))
		case "type":
			for _, name := range args[1:] {
				if builtInCommands[name] {
					_, _ = fmt.Fprintln(stdout, name+" is a shell builtin")
				} else if path, err := exec.LookPath(name); err == nil {
					_, _ = fmt.Fprintln(stdout, name+" is "+path)
				} else {
					_, _ = fmt.Fprintln(os.Stderr, name+": not found")
				}
			}
		case "pwd":
			if dir, err := os.Getwd(); err == nil {
				_, _ = fmt.Fprintln(stdout, dir)
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
				_, _ = fmt.Fprintln(os.Stderr, args[0]+": command not found")
			} else {
				cmd := exec.Command(args[0], args[1:]...)
				cmd.Stdin = os.Stdin
				cmd.Stdout = stdout
				cmd.Stderr = os.Stderr
				_ = cmd.Run()
			}
		}
		if stdout != os.Stdout {
			_ = stdout.Close()
		}
	}
}

// parseCommand splits line into arguments at unquoted spaces and tabs, and
// returns the file named after an unquoted > or 1>, where standard output
// should go. Quoted text is taken literally, except that inside double quotes
// a backslash escapes ", \, $ and `. Outside quotes, a backslash makes the next
// character literal. Adjacent pieces join into one argument.
func parseCommand(line string) (args []string, stdoutFile string) {
	var arg strings.Builder
	inArg := false    // true once an argument has started, even an empty one like ''
	plain := true     // the argument so far has no quotes or backslashes
	var quote rune    // the quote character we're inside, or 0 when outside quotes
	escaped := false  // the previous character was a backslash outside single quotes
	redirect := false // the next argument is the file for standard output
	endArg := func() {
		if !inArg {
			return
		}
		if redirect {
			stdoutFile, redirect = arg.String(), false
		} else {
			args = append(args, arg.String())
		}
		arg.Reset()
		inArg, plain = false, true
	}
	for _, r := range line {
		switch {
		case escaped:
			if quote == '"' && !strings.ContainsRune("\"\\$`", r) {
				arg.WriteRune('\\') // not an escape inside double quotes, so keep it
			}
			arg.WriteRune(r)
			escaped, inArg = false, true
		case r == '\\' && quote != '\'':
			escaped, plain = true, false
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
			arg.WriteRune(r)
		case r == '\'' || r == '"':
			quote, inArg, plain = r, true, false
		case r == '>':
			if plain && arg.String() == "1" {
				arg.Reset() // an unquoted 1 right before > names standard output
				inArg = false
			}
			endArg()
			redirect = true
		case r == ' ' || r == '\t':
			endArg()
		default:
			arg.WriteRune(r)
			inArg = true
		}
	}
	endArg()
	return args, stdoutFile
}
