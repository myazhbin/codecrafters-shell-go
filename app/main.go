package main

import (
	"bufio"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/chzyer/readline"
)

var builtInCommands = map[string]bool{
	"exit": true,
	"echo": true,
	"type": true,
	"pwd":  true,
	"cd":   true,
}

func main() {
	// Input from a file or pipe is read as plain lines, since readline would
	// treat a Tab in it as a request to complete.
	reader := bufio.NewReader(os.Stdin)
	readLine := func() (string, error) {
		fmt.Print("$ ")
		line, err := reader.ReadString('\n')
		return strings.TrimRight(line, "\r\n"), err
	}
	if readline.DefaultIsTerminal() {
		rl, err := readline.NewEx(&readline.Config{
			Prompt:       "$ ",
			AutoComplete: builtinCompleter{},
			FuncGetWidth: terminalWidth,
		})
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "Error setting up input:", err)
			os.Exit(1)
		}
		defer rl.Close()
		readLine = rl.Readline
	}
	for {
		command, err := readLine()
		if errors.Is(err, readline.ErrInterrupt) {
			continue // Ctrl-C drops the line being typed
		}
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "Error reading input:", err)
			os.Exit(1)
		}
		if runCommand(command) {
			return
		}
	}
}

// builtinCompleter completes the command name at the start of the line to a
// builtin, followed by a space, and rings the bell when nothing matches.
type builtinCompleter struct{}

func (builtinCompleter) Do(line []rune, pos int) (newLine [][]rune, length int) {
	word := strings.TrimLeft(string(line[:pos]), " ")
	if !strings.Contains(word, " ") { // only the command name is completed
		for _, name := range slices.Sorted(maps.Keys(builtInCommands)) {
			if strings.HasPrefix(name, word) {
				newLine = append(newLine, []rune(name[len(word):]+" "))
			}
		}
	}
	if len(newLine) == 0 {
		fmt.Print("\a")
	}
	return newLine, len(word)
}

// terminalWidth returns the terminal's width in columns, or 80 when it reports
// none, since readline turns Tab completion off for a width of 0.
func terminalWidth() int {
	if width := readline.GetScreenWidth(); width > 0 {
		return width
	}
	return 80
}

// runCommand runs one command line and reports whether the shell should exit.
func runCommand(line string) (exit bool) {
	args, stdoutFile, stderrFile := parseCommand(line)
	if len(args) == 0 {
		return false
	}
	stdout, stderr := os.Stdout, os.Stderr
	if stdoutFile.path != "" {
		file, err := stdoutFile.open()
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			return false
		}
		defer file.Close()
		stdout = file
	}
	if stderrFile.path != "" {
		file, err := stderrFile.open()
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

// redirection says where standard output or standard error goes: the file at
// path, emptied first unless append is set, or the terminal when path is "".
type redirection struct {
	path   string
	append bool
}

// open opens the redirection's file for writing, creating it if needed.
func (r redirection) open() (*os.File, error) {
	flag := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if r.append {
		flag = os.O_WRONLY | os.O_CREATE | os.O_APPEND
	}
	return os.OpenFile(r.path, flag, 0o666)
}

// parseCommand splits line into arguments at unquoted spaces and tabs, and
// picks out where standard output (> or 1>) and standard error (2>) go; a
// doubled >> appends to the file instead. Quoted text is taken literally,
// except that inside double quotes a backslash escapes ", \, $ and `. Outside
// quotes, a backslash makes the next character literal. Adjacent pieces join
// into one argument.
func parseCommand(line string) (args []string, stdoutFile, stderrFile redirection) {
	var arg strings.Builder
	inArg := false          // true once an argument has started, even an empty one like ''
	plain := true           // the argument so far has no quotes or backslashes
	var quote rune          // the quote character we're inside, or 0 when outside quotes
	escaped := false        // the previous character was a backslash outside single quotes
	var target *redirection // the redirection whose file the next argument names
	afterGreater := false   // the previous character was a > that started a redirection
	endArg := func() {
		if !inArg {
			return
		}
		if target != nil {
			target.path, target = arg.String(), nil
		} else {
			args = append(args, arg.String())
		}
		arg.Reset()
		inArg, plain = false, true
	}
	for _, r := range line {
		doubled := afterGreater && r == '>'
		afterGreater = false
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
		case doubled:
			target.append = true
		case r == '>':
			next := &stdoutFile
			if n := arg.String(); plain && (n == "1" || n == "2") {
				if n == "2" {
					next = &stderrFile
				}
				arg.Reset() // an unquoted 1 or 2 right before > picks the output
				inArg = false
			}
			endArg()
			*next = redirection{} // replaces any earlier redirection of the same output
			target, afterGreater = next, true
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
