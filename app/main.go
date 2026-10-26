package main

import (
	"bufio"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
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
		completer := &tabCompleter{}
		rl, err := readline.NewEx(&readline.Config{
			Prompt:       "$ ",
			AutoComplete: completer,
			Listener:     completer,
			FuncGetWidth: terminalWidth,
		})
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "Error setting up input:", err)
			os.Exit(1)
		}
		defer rl.Close()
		completer.rl = rl
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

// tabCompleter completes the word before the cursor: the command name to a
// builtin or a program in PATH, and any other word to a name in the current
// directory. A completed name gets a space after it, or a "/" for a directory.
// Like bash, it rings the bell when nothing matches. When several names match,
// it adds what they all start with; if that's nothing more, the first Tab rings
// the bell and a second Tab in a row lists them.
type tabCompleter struct {
	rl        *readline.Instance
	ambiguous bool // the last key was a Tab that found several matches
}

func (c *tabCompleter) Do(line []rune, pos int) (newLine [][]rune, length int) {
	typed := string(line[:pos])
	word := typed[strings.LastIndex(typed, " ")+1:]
	var names []string
	if strings.TrimLeft(typed, " ") == word {
		names = commandNames(word)
	} else {
		names = fileNames(word)
	}
	prefix := word
	if len(names) > 1 {
		prefix = commonPrefix(names)
	}
	switch {
	case len(names) == 1:
		completion := names[0][len(word):]
		if !strings.HasSuffix(completion, "/") {
			completion += " "
		}
		return [][]rune{[]rune(completion)}, len([]rune(word))
	case len(prefix) > len(word): // the matches all go on past what was typed
		return [][]rune{[]rune(prefix[len(word):])}, len([]rune(word))
	case len(names) > 1 && c.ambiguous:
		fmt.Print("\n" + strings.Join(names, "  ") + "\n")
		c.rl.Refresh() // show the prompt and the line again below the list
	default:
		fmt.Print("\a")
		c.ambiguous = len(names) > 1
	}
	return nil, 0
}

// OnChange is called by readline after every key, and with key 0 when it starts
// reading a line. Any key but Tab ends a run of Tabs.
func (c *tabCompleter) OnChange(line []rune, pos int, key rune) (newLine []rune, newPos int, ok bool) {
	if key != '\t' {
		c.ambiguous = false
	}
	return nil, 0, false
}

// commandNames returns the builtins and the programs in PATH whose names start
// with prefix, sorted and without duplicates.
func commandNames(prefix string) []string {
	names := map[string]bool{}
	for name := range builtInCommands {
		if strings.HasPrefix(name, prefix) {
			names[name] = true
		}
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue // PATH can list directories that don't exist
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasPrefix(name, prefix) || names[name] {
				continue
			}
			info, err := os.Stat(filepath.Join(dir, name))
			if err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
				names[name] = true
			}
		}
	}
	return slices.Sorted(maps.Keys(names))
}

// fileNames returns the names in the current directory that start with prefix,
// sorted, with a "/" after each directory.
func fileNames(prefix string) []string {
	entries, err := os.ReadDir(".")
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		if entry.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	return names
}

// commonPrefix returns the longest prefix shared by sorted names, which is the
// one shared by the first and the last.
func commonPrefix(sorted []string) string {
	first, last := []rune(sorted[0]), []rune(sorted[len(sorted)-1])
	n := 0
	for n < len(first) && n < len(last) && first[n] == last[n] {
		n++
	}
	return string(first[:n])
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
