package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/chzyer/readline"
)

var builtInCommands = map[string]bool{
	"exit":     true,
	"echo":     true,
	"type":     true,
	"pwd":      true,
	"cd":       true,
	"complete": true,
	"jobs":     true,
	"history":  true,
}

// completers maps a command name to the program registered with complete -C to
// suggest completions for that command, until complete -r removes it.
var completers = map[string]string{}

// job is a command line run in the background with &: a program, or a pipeline
// of them.
type job struct {
	number  int
	command string        // the command line that started it, without the &
	done    chan struct{} // closed once the program has exited
}

// jobs are the background jobs, oldest first.
var jobs []job

// history holds the command lines entered so far, oldest first, for the
// history builtin. As in bash, blank lines aren't kept.
var history []string

// remember adds a line to the list the Up arrow steps through, which readline
// keeps, or does nothing when the shell isn't reading from a terminal.
var remember = func(line string) {}

// addHistory adds line to history, and for the Up arrow. Readline doesn't save
// typed lines itself: it would do that from its own goroutine after handing a
// line over, racing whatever the shell adds, such as the lines history -r reads.
func addHistory(line string) {
	history = append(history, line)
	remember(line)
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
			// addHistory saves the lines instead.
			DisableAutoSaveHistory: true,
		})
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "Error setting up input:", err)
			os.Exit(1)
		}
		defer rl.Close()
		completer.rl = rl
		readLine = rl.Readline
		remember = func(line string) { _ = rl.SaveHistory(line) }
	}
	for {
		reapJobs(os.Stderr, false) // as in bash, jobs that have ended are reported before each prompt
		command, err := readLine()
		if errors.Is(err, readline.ErrInterrupt) {
			continue // Ctrl-C drops the line being typed
		}
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "Error reading input:", err)
			os.Exit(1)
		}
		if strings.TrimSpace(command) != "" {
			addHistory(command) // before it runs, so history lists itself
		}
		if runCommand(command) {
			return
		}
	}
}

// tabCompleter completes the word before the cursor: the command name to a
// builtin or a program in PATH, and any other word to a line printed by the
// command's completer (see complete -C) or, without one, to a file name, in
// the directory the word names up to its last "/" or else the current
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
	// The words before word; none when word is the command name.
	before := strings.Fields(typed[:len(typed)-len(word)])
	var partial string // the end of word that the names complete
	var names []string
	if len(before) == 0 {
		partial, names = word, commandNames(word)
	} else if script, ok := completers[before[0]]; ok {
		partial, names = word, completerNames(script, before, word, string(line), len(typed))
	} else {
		dir := word[:strings.LastIndex(word, "/")+1] // "" when word has no "/"
		partial = word[len(dir):]
		names = fileNames(dir, partial)
	}
	prefix := partial
	if len(names) > 1 {
		prefix = commonPrefix(names)
	}
	switch {
	case len(names) == 1:
		completion := names[0][len(partial):]
		if !strings.HasSuffix(completion, "/") {
			completion += " "
		}
		return [][]rune{[]rune(completion)}, len([]rune(partial))
	case len(prefix) > len(partial): // the matches all go on past what was typed
		return [][]rune{[]rune(prefix[len(partial):])}, len([]rune(partial))
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

// completerNames runs script, a completer registered with complete -C, for
// completing word after the words before it in commandLine, with the cursor at
// byte offset point, and returns the lines it prints that start with word,
// sorted and without duplicates. As in bash, the script gets the command name,
// word, and the word before word (the command name for the first argument) as
// arguments, and commandLine and point in COMP_LINE and COMP_POINT.
func completerNames(script string, before []string, word, commandLine string, point int) []string {
	cmd := exec.Command(script, before[0], word, before[len(before)-1])
	cmd.Env = append(os.Environ(), "COMP_LINE="+commandLine, fmt.Sprintf("COMP_POINT=%d", point))
	out, _ := cmd.Output() // a failed script prints nothing useful
	var names []string
	for _, name := range strings.Split(string(out), "\n") {
		name = strings.TrimRight(name, "\r")
		if name != "" && strings.HasPrefix(name, word) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// fileNames returns the names in dir (the current directory when dir is "") that
// start with prefix, sorted, with a "/" after each directory.
func fileNames(dir, prefix string) []string {
	if dir == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
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

// runCommand runs one command line, a builtin or a pipeline, and reports
// whether the shell should exit. Only programs run in the background; a builtin
// ending in & runs in the shell as usual.
func runCommand(line string) (exit bool) {
	commands, background := parseCommand(line)
	for _, c := range commands {
		if len(c.args) == 0 {
			if len(commands) > 1 {
				_, _ = fmt.Fprintln(os.Stderr, "syntax error near unexpected token `|'")
			}
			return false
		}
	}
	if len(commands) == 1 && builtInCommands[commands[0].args[0]] {
		return runBuiltin(commands[0], os.Stdout)
	}
	runPipeline(commands, background, line)
	return false
}

// runBuiltin runs c, a builtin, writing to stdout unless c redirects its
// output, and reports whether the shell should exit.
func runBuiltin(c command, stdout *os.File) (exit bool) {
	var files []*os.File
	defer func() { closeAll(files) }()
	stdout, stderr, err := c.openOutputs(stdout, os.Stderr, &files)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		return false
	}
	args := c.args
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
	case "complete":
		switch {
		case len(args) > 1 && args[1] == "-p":
			for _, name := range args[2:] {
				if script, ok := completers[name]; ok {
					_, _ = fmt.Fprintf(stdout, "complete -C '%s' %s\n", script, name)
				} else {
					_, _ = fmt.Fprintln(stderr, "complete: "+name+": no completion specification")
				}
			}
		case len(args) > 2 && args[1] == "-C":
			for _, name := range args[3:] {
				completers[name] = args[2]
			}
		case len(args) > 1 && args[1] == "-r":
			if len(args) == 2 {
				clear(completers) // as in bash, no names means every command
			}
			for _, name := range args[2:] {
				delete(completers, name) // unlike bash, says nothing when there's none
			}
		}
	case "jobs":
		reapJobs(stdout, true)
	case "history":
		runHistory(args[1:], stdout, stderr)
	}
	return false
}

// runHistory runs the history builtin with args. With none, it lists the
// whole history, numbered from 1, and with a number n, just the last n
// entries. As in bash, -r adds the lines of a file to history, skipping empty
// ones, and -w writes history to a file, a line per entry, replacing what the
// file held or creating it readable only by its owner. The file is $HISTFILE
// unless one is named, and one that can't be read or written is passed over
// without a word.
func runHistory(args []string, stdout, stderr io.Writer) {
	if len(args) > 0 && (args[0] == "-r" || args[0] == "-w") {
		file := os.Getenv("HISTFILE")
		if len(args) > 1 {
			file = args[1]
		}
		switch {
		case file == "":
			_, _ = fmt.Fprintln(stderr, "history: HISTFILE: parameter null or not set")
		case args[0] == "-r":
			data, _ := os.ReadFile(file)
			for _, line := range strings.Split(string(data), "\n") {
				if line = strings.TrimSuffix(line, "\r"); line != "" {
					addHistory(line)
				}
			}
		default:
			var lines strings.Builder
			for _, line := range history {
				lines.WriteString(line + "\n")
			}
			_ = os.WriteFile(file, []byte(lines.String()), 0o600)
		}
		return
	}
	first := 0 // the index of the first entry to list
	if len(args) > 0 {
		n, err := strconv.Atoi(args[0])
		if err != nil || n < 0 {
			_, _ = fmt.Fprintln(stderr, "history: "+args[0]+": numeric argument required")
			return
		}
		first = max(len(history)-n, 0)
	}
	for i := first; i < len(history); i++ {
		_, _ = fmt.Fprintf(stdout, "%5d  %s\n", i+1, history[i])
	}
}

// runPipeline runs commands as a pipeline: each one's standard output goes
// through a pipe to the next one's standard input, unless it's redirected.
// Programs run side by side, and then builtins run in the shell itself, one at
// a time. No builtin reads its input, so a program writing to one gets SIGPIPE,
// as with any program that doesn't read. Bash runs a pipeline's builtins in
// subshells, so here too exit doesn't end the shell, though cd does change its
// directory. It waits for the programs to finish or, with background, makes
// them a job for line. As in bash, a program that can't be run is reported and
// the rest of the pipeline runs without it.
func runPipeline(commands []command, background bool, line string) {
	// Command i reads from in[i] and writes to out[i], unless it redirects them.
	in, out := make([]*os.File, len(commands)), make([]*os.File, len(commands))
	in[0], out[len(commands)-1] = os.Stdin, os.Stdout
	var pipes []*os.File
	for i := range len(commands) - 1 {
		r, w, err := os.Pipe()
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			closeAll(pipes)
			return
		}
		pipes = append(pipes, r, w)
		out[i], in[i+1] = w, r
	}
	var files []*os.File // the files programs' output is redirected to
	var cmds []*exec.Cmd
	for i, c := range commands {
		if !builtInCommands[c.args[0]] {
			if cmd := startProgram(c, in[i], out[i], &files); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	}
	// The programs have their own copies of these now. Once the shell lets go
	// of a pipe, its reader sees the end of its input when the writer exits, and
	// the writer gets SIGPIPE once the reader has exited, as in tail -f | head.
	// The shell keeps only the pipes builtins write to, until they're done.
	closeAll(files)
	for i, c := range commands {
		if i > 0 {
			_ = in[i].Close()
		}
		if i < len(commands)-1 && !builtInCommands[c.args[0]] {
			_ = out[i].Close()
		}
	}
	for i, c := range commands {
		if builtInCommands[c.args[0]] {
			runBuiltin(c, out[i])
			if i < len(commands)-1 {
				_ = out[i].Close()
			}
		}
	}
	if background && len(cmds) > 0 {
		addJob(cmds, line)
		return
	}
	for _, cmd := range cmds {
		_ = cmd.Wait()
	}
}

// startProgram starts c, a program, reading from stdin and writing to stdout
// and the shell's standard error unless c redirects them, and returns it, or
// reports why it couldn't and returns nil. It adds the files it opens to files,
// for the caller to close.
func startProgram(c command, stdin, stdout *os.File, files *[]*os.File) *exec.Cmd {
	stdout, stderr, err := c.openOutputs(stdout, os.Stderr, files)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		return nil
	}
	if _, err := exec.LookPath(c.args[0]); err != nil {
		_, _ = fmt.Fprintln(stderr, c.args[0]+": command not found")
		return nil
	}
	cmd := exec.Command(c.args[0], c.args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	if err := cmd.Start(); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return nil
	}
	return cmd
}

// addJob makes cmds, the started programs of line, a command line ending in &,
// a job. Like bash, it numbers the job one more than the newest one, or 1 if
// there are none, and reports the number and the last program's process ID on
// the shell's standard error.
func addJob(cmds []*exec.Cmd, line string) {
	number := 1
	if len(jobs) > 0 {
		number = jobs[len(jobs)-1].number + 1
	}
	text := strings.TrimSpace(line[:strings.LastIndex(line, "&")])
	j := job{number, text, make(chan struct{})}
	jobs = append(jobs, j)
	_, _ = fmt.Fprintf(os.Stderr, "[%d] %d\n", number, cmds[len(cmds)-1].Process.Pid)
	go func() {
		for _, cmd := range cmds {
			_ = cmd.Wait() // reaps it once it exits
		}
		close(j.done)
	}()
}

// reapJobs lists to w, the way bash's jobs does, each job that has ended, as
// Done, and drops it; with listRunning, it lists the running jobs too. The
// newest job is marked + and the one before it -.
func reapJobs(w io.Writer, listRunning bool) {
	var left []job
	for i, j := range jobs {
		marker := " "
		if i == len(jobs)-1 {
			marker = "+"
		} else if i == len(jobs)-2 {
			marker = "-"
		}
		select {
		case <-j.done:
			_, _ = fmt.Fprintf(w, "[%d]%s  %-24s%s\n", j.number, marker, "Done", j.command)
		default:
			if listRunning {
				_, _ = fmt.Fprintf(w, "[%d]%s  %-24s%s &\n", j.number, marker, "Running", j.command)
			}
			left = append(left, j)
		}
	}
	jobs = left
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

// command is one command of a pipeline: its arguments, and where its standard
// output and standard error go.
type command struct {
	args           []string
	stdout, stderr redirection
}

// openOutputs opens the files c redirects its standard output and standard
// error to, adding them to files for the caller to close, and returns them in
// place of stdout and stderr.
func (c command) openOutputs(stdout, stderr *os.File, files *[]*os.File) (*os.File, *os.File, error) {
	if c.stdout.path != "" {
		file, err := c.stdout.open()
		if err != nil {
			return nil, nil, err
		}
		*files = append(*files, file)
		stdout = file
	}
	if c.stderr.path != "" {
		file, err := c.stderr.open()
		if err != nil {
			return nil, nil, err
		}
		*files = append(*files, file)
		stderr = file
	}
	return stdout, stderr, nil
}

// closeAll closes files.
func closeAll(files []*os.File) {
	for _, file := range files {
		_ = file.Close()
	}
}

// parseCommand splits line at unquoted |s into the commands of a pipeline, and
// each command into arguments at unquoted spaces and tabs, picking out where
// its standard output (> or 1>) and standard error (2>) go; a doubled >>
// appends to the file instead. An unquoted & ending the line sets background.
// Quoted text is taken literally, except that inside double quotes a backslash
// escapes ", \, $ and `. Outside quotes, a backslash makes the next character
// literal. Adjacent pieces join into one argument.
func parseCommand(line string) (commands []command, background bool) {
	var cur command // the command being parsed
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
			cur.args = append(cur.args, arg.String())
		}
		arg.Reset()
		inArg, plain = false, true
	}
	for i, r := range line {
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
			next := &cur.stdout
			if n := arg.String(); plain && (n == "1" || n == "2") {
				if n == "2" {
					next = &cur.stderr
				}
				arg.Reset() // an unquoted 1 or 2 right before > picks the output
				inArg = false
			}
			endArg()
			*next = redirection{} // replaces any earlier redirection of the same output
			target, afterGreater = next, true
		case r == '|':
			endArg()
			commands = append(commands, cur)
			cur, target = command{}, nil // a > right before the | names no file
		case r == '&' && strings.TrimSpace(line[i+1:]) == "":
			endArg()
			background = true
		case r == ' ' || r == '\t':
			endArg()
		default:
			arg.WriteRune(r)
			inArg = true
		}
	}
	endArg()
	return append(commands, cur), background
}
