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
		if runCommand(strings.TrimRight(command, "\r\n")) {
			return
		}
	}
}

// runCommand runs one command line and reports whether the shell should exit.
func runCommand(line string) (exit bool) {
	args, stdoutFile, stderrFile := parseCommand(line)
	if len(args) == 0 {
		return false
	}
	stdout, stderr := os.Stdout, os.Stderr
	if stdoutFile != "" {
		file, err := os.Create(stdoutFile)
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			return false
		}
		defer file.Close()
		stdout = file
	}
	if stderrFile != "" {
		file, err := os.Create(stderrFile)
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			return false
		}
		defer file.Close()
		stderr = file
	}
	switch args[0] {
	case "exit":
		return true
	case "echo":
		_, _ = fmt.Fprintln(stdout, strings.Join(args[1:], " "))
	case "type":
		for _, name := range args[1:] {
			if builtInCommands[name] {
				_, _ = fmt.Fprintln(stdout, name+" is a shell builtin")
			} else if path, err := exec.LookPath(name); err == nil {
				_, _ = fmt.Fprintln(stdout, name+" is "+path)
			} else {
				_, _ = fmt.Fprintln(stderr, name+": not found")
			}
		}
	case "pwd":
		if dir, err := os.Getwd(); err == nil {
			_, _ = fmt.Fprintln(stdout, dir)
		} else {
			_, _ = fmt.Fprintln(stderr, "pwd:", err)
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
			_, _ = fmt.Fprintln(stderr, "cd: "+dir+": No such file or directory")
		} else if err != nil {
			_, _ = fmt.Fprintln(stderr, "cd:", err)
		}
	default:
		if _, err := exec.LookPath(args[0]); err != nil {
			_, _ = fmt.Fprintln(stderr, args[0]+": command not found")
		} else {
			cmd := exec.Command(args[0], args[1:]...)
			cmd.Stdin = os.Stdin
			cmd.Stdout = stdout
			cmd.Stderr = stderr
			_ = cmd.Run()
		}
	}
	return false
}

// parseCommand splits line into arguments at unquoted spaces and tabs, and
// returns the files named after an unquoted > or 1> (for standard output) and
// 2> (for standard error). Quoted text is taken literally, except that inside
// double quotes a backslash escapes ", \, $ and `. Outside quotes, a backslash
// makes the next character literal. Adjacent pieces join into one argument.
func parseCommand(line string) (args []string, stdoutFile, stderrFile string) {
	var arg strings.Builder
	inArg := false   // true once an argument has started, even an empty one like ''
	plain := true    // the argument so far has no quotes or backslashes
	var quote rune   // the quote character we're inside, or 0 when outside quotes
	escaped := false // the previous character was a backslash outside single quotes
	redirect := 0    // 1 or 2 when the next argument names the file for that output
	endArg := func() {
		if !inArg {
			return
		}
		switch redirect {
		case 1:
			stdoutFile = arg.String()
		case 2:
			stderrFile = arg.String()
		default:
			args = append(args, arg.String())
		}
		arg.Reset()
		inArg, plain, redirect = false, true, 0
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
			fd := 1
			if n := arg.String(); plain && (n == "1" || n == "2") {
				fd = int(n[0] - '0') // an unquoted 1 or 2 right before > picks the output
				arg.Reset()
				inArg = false
			}
			endArg()
			redirect = fd
		case r == ' ' || r == '\t':
			endArg()
		default:
			arg.WriteRune(r)
			inArg = true
		}
	}
	endArg()
	return args, stdoutFile, stderrFile
}
