// Tests for the shell. TestMain builds it, and the tests run it as a separate
// program the way a person or the CodeCrafters tester would: mostly on a
// pseudo-terminal, so line editing, Tab completion and job control behave as
// they do for real, and partly by piping the same command lines into it and
// into bash and comparing what they print. Every stage gets tests here.
//
// Run them from the repository's root with: go test ./app/
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

const (
	columns = 300              // the terminal's width: wide, so long command lines don't wrap
	timeout = 10 * time.Second // how long to wait on the shell, which the race detector slows
)

// shellPath is the shell under test, built by TestMain.
var shellPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "shell-test")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	shellPath = filepath.Join(dir, "shell")
	// Jobs end in goroutines, so build with the race detector when the
	// toolchain can; a session fails if its shell reports a race.
	if out, err := exec.Command("go", "build", "-race", "-o", shellPath, ".").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building without the race detector, which failed: %s\n", out)
		if out, err := exec.Command("go", "build", "-o", shellPath, ".").CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "building the shell: %v\n%s", err, out)
			os.Exit(1)
		}
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// shellEnv is the environment the shell runs in, plus env: programs come from
// the standard places only, so results don't depend on the person's PATH.
func shellEnv(env ...string) []string {
	return append([]string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + os.Getenv("HOME"), "TERM=xterm-256color"}, env...)
}

// Bash and the shell print the same thing for these command lines, apart from
// how bash starts its error messages.

func TestScriptsMatchBash(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash isn't installed")
	}
	for _, tc := range []struct{ name, script string }{
		{"quoting and redirection", `echo hello   world
echo 'single  quoted' "double  quoted" mixed' 'pieces
echo "a\"b" 'c\d' e\ f "x\y"
echo hi > out1.txt
echo there >> out1.txt
cat out1.txt
echo two 1> out2.txt
cat out2.txt
cat nosuchfile 2> err.txt
cat err.txt
echo '&' "&" \& a\&b
echo 'ends with &'
echo ends with \&
type echo jobs history declare sleep
nosuchcmd 2> nf.txt
cat nf.txt
`},
		{"pipelines", `cat input.txt | wc
cat input.txt|wc -l
printf '3\n1\n2\n' | sort
printf '3\n1\n2\n' | sort | head -n 2
seq 1 200000 | tail -n 1
yes | head -n 3
echo 'a|b' "c|d" e\|f
cat nosuchfile | wc -l
nosuchcmd | wc -l
printf 'x\ny\n' | wc -l > count.txt
cat count.txt
cat nosuchfile 2> err.txt | wc -l
cat err.txt
printf 'p\nq\n' | cat > copy.txt
cat copy.txt
printf 'b\na\n' | sort >> copy.txt
cat copy.txt
`},
		{"builtins in pipelines", `echo apple-orange | wc
ls | type exit
echo hello | wc -c
echo a | type echo | wc -l
pwd | wc -l
echo one | cat | cat
type exit type | cat
exit | cat
echo still here
`},
		{"longer pipelines", `cat input.txt | head -n 3 | wc
echo a > file-a
echo b > file-b
echo c > other
ls | tail -n 3 | head -n 2 | grep file
cat input.txt | sort | head -n 4 | tail -n 2 | wc -l
printf 'x\n' | cat | cat | cat | cat
nosuchcmd | cat | wc -l
echo hi | nosuchcmd | wc -l
type echo | cat | wc -l
`},
		{"declare", `declare -p missing_variable
declare -p one two 2> err.txt
cat err.txt
declare foo=bar
declare -p foo
declare foo=bar2 other=x
declare -p missing foo other
declare quoted='a"b\c$d` + "`" + `e!f'"'"'g' spaced="two  words" eq=a=b empty=
declare -p foo=bar quoted spaced eq empty
declare 1x=2 =y a-b=1 ok=1 _FOO=BAR x9_=1 67=x
declare -p 1x ok _FOO x9_
declare ap=one
declare ap+=two new+=three
declare -p ap new
`},
		{"variables", `declare Variable_1=value Variable_2=value2 empty= spaced="  a   b  " y="a b" cmd="echo hi" fd=2 q='a"b\c'
echo $Variable_1 $Variable_2
printf '[%s]\n' a $empty b $empty$empty
printf '[%s]\n' a "$empty" b ''$empty
printf '[%s]\n' $spaced
printf '[%s]\n' x$spaced"y"
printf '[%s]\n' "x$spaced"
printf '[%s]\n' $Variable_1$Variable_2 $Variable_1-suffix $Variable_1_suffix
printf '[%s]\n' $ a$ "$" "a$ b"
printf '[%s]\n' "$Variable_1"'$Variable_1'\$Variable_1 "\$Variable_1"
printf '[%s]\n' $q
$cmd there
declare x=$y
declare -p x
declare z="p=1 q2=2"
declare $z
declare -p p q2
declare w=$spaced
declare -p w
echo $fd> f1.txt
cat f1.txt
echo out > $y
echo out > $empty
echo out > $Variable_1
cat value
`},
		{"no newline at the end", "echo one\necho two"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Without a terminal the shell still prompts, on the line its output
			// then continues.
			ours := regexp.MustCompile(`(?m)^(\$ )+`).ReplaceAllString(runScript(t, shellPath, tc.script), "")
			theirs := regexp.MustCompile(`(?m)^\S*bash: line \d+: `).ReplaceAllString(runScript(t, bash, tc.script, "--norc", "--noprofile"), "")
			if strings.TrimSpace(ours) != strings.TrimSpace(theirs) {
				t.Errorf("the shell printed\n%s\nbash printed\n%s", ours, theirs)
			}
		})
	}
}

// input is the file input.txt that runScript's scripts can read.
const input = "raspberry strawberry\npear mango\npineapple apple\nbanana grape\nkiwi lemon\n"

// runScript runs program with args, reading the command lines in script
// without a terminal until their end, which ends it as exit would, in a new
// directory holding input.txt, and returns everything it writes.
func runScript(t *testing.T, program, script string, args ...string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "input.txt"), []byte(input), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Dir, cmd.Env = dir, shellEnv()
	cmd.Stdin = strings.NewReader(script)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out // one pipe, so messages land in order
	cmd.WaitDelay = time.Second
	_ = cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("%s hung running:\n%s", program, script)
	}
	return out.String()
}

// Tab completion.

func TestCompletion(t *testing.T) {
	t.Parallel()
	for _, completer := range []struct{ name, script string }{
		{"completer filters", `for c in checkout cherry-pick; do case "$c" in "$2"*) echo "$c" ;; esac; done` + "\n"},
		{"completer prints all", "echo checkout\necho cherry-pick\n"},
	} {
		t.Run(completer.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir() // with two files to complete when there's no completer
			for _, name := range []string{"apple.txt", "banana.txt"} {
				if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			script := writeScript(t, t.TempDir(), "complete-git", completer.script)
			s := startShell(t, dir)
			s.expect("complete -C " + script + " git")
			s.expect("complete -p git", "complete -C '"+script+"' git")
			s.tab("git c\t", "$ git che", false)   // both candidates start with che
			s.tab("c\t", "$ git checkout ", false) // only checkout is left: the whole word and a space
			s.interrupt()
			s.tab("git che\t", "$ git che", true) // nothing more in common: the bell
			s.send("\t")                          // and a second Tab lists them
			s.waitFor("the candidates listed", func() bool {
				return s.screen.text(s.screen.row-1) == "checkout  cherry-pick" && s.screen.cursorLine() == "$ git che"
			})
			s.interrupt()
			s.expect("complete -r git")
			s.expect("complete -p git", "complete: git: no completion specification")
			s.tab("git \t", "$ git ", true) // back to file names, as if no completer was ever registered
			s.send("\t")
			s.waitFor("the files listed", func() bool {
				return s.screen.text(s.screen.row-1) == "apple.txt  banana.txt" && s.screen.cursorLine() == "$ git "
			})
			s.interrupt()
			s.expect("complete -r nothere") // the stage wants no error, unlike bash
			s.expect("complete -C " + script + " git")
			s.expect("complete -C " + script + " svn")
			s.expect("complete -r") // as in bash, no names removes them all
			s.expect("complete -p git svn", "complete: git: no completion specification", "complete: svn: no completion specification")
		})
	}
}

func TestCompleterArguments(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	script := writeScript(t, dir, "complete-git", `printf '%s|%s|%s|%s|%s\n' "$1" "$2" "$3" "$COMP_LINE" "$COMP_POINT" >> "$COMPLETER_LOG"`+"\n")
	s := startShell(t, dir, "COMPLETER_LOG="+log)
	s.expect("complete -C " + script + " git")
	s.tab("git ch\t", "$ git ch", true) // it suggests nothing
	s.interrupt()
	s.tab("git commit -m\t", "$ git commit -m", true)
	s.interrupt()
	got, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	// As in bash: the command, the word, and the word before it, which is the
	// command itself for the first argument; and the line and cursor position.
	if want := "git|ch|git|git ch|6\ngit|-m|commit|git commit -m|13\n"; string(got) != want {
		t.Errorf("the completer got\n%swant\n%s", got, want)
	}
}

// Background jobs.

func TestBackgroundJobs(t *testing.T) {
	t.Parallel()
	s := startShell(t, t.TempDir())
	began := time.Now()
	sleeper := s.startJob("sleep 500 &", 1)
	if took := time.Since(began); took > 5*time.Second {
		t.Errorf("the prompt took %v to come back", took)
	}
	if parent, command := parentAndCommand(t, sleeper); parent != s.cmd.Process.Pid || command != "sleep 500" {
		t.Errorf("job 1 is %q, a child of %d; want sleep 500, a child of the shell (%d)", command, parent, s.cmd.Process.Pid)
	}
	s.expect("echo still here", "still here")
	short := s.startJob("sleep 1&", 2)            // no space before the &
	s.expect(`echo '&' "&" \& a\&b`, "& & & a&b") // quoted or escaped, & is just a character
	waitGone(t, short)                            // reaped once it ends, not left a zombie
	s.expect("", "[2]+  Done                    sleep 1")
	talker := s.startJob("sh -c 'sleep 0.3; echo from the background' &", 2)
	s.waitText("from the background") // shown while the shell waits at its prompt
	waitGone(t, talker)
	s.expect("", "[2]+  Done                    sh -c 'sleep 0.3; echo from the background'")
	writer := s.startJob("sh -c 'echo into a file' > bg.txt &", 2)
	waitGone(t, writer)
	s.expect("cat bg.txt", "into a file", "[2]+  Done                    sh -c 'echo into a file' > bg.txt")
}

func TestBackgroundOutput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fifo1, fifo2 := mkfifo(t, dir, "fifo1"), mkfifo(t, dir, "fifo2")
	s := startShell(t, dir)
	background := s.startJob("cat "+fifo1+" &", 1)
	start := s.row()
	s.send("cat " + fifo2 + "\r")
	// Both cats have the shell's terminal as their standard input, output and error.
	shell := s.cmd.Process.Pid
	if want, foreground := stdio(shell), waitChild(t, shell, background); want != nil && foreground != 0 {
		for _, pid := range []int{background, foreground} {
			if got := stdio(pid); !slices.Equal(got, want) {
				t.Errorf("process %d has %q open as its standard input, output and error; the shell has %q", pid, got, want)
			}
		}
	}
	writeFIFO(t, fifo1, "Hello from FIFO#1\n")
	s.waitText("Hello from FIFO#1")
	waitGone(t, background)
	writeFIFO(t, fifo2, "Hello from FIFO#2\n")
	sameLines(t, "the two cats", s.waitPrompt(start, "cat "+fifo2),
		"Hello from FIFO#1", "Hello from FIFO#2", listed("[1]+", "Done", "cat "+fifo1))
	s.expect("echo I can type this immediately", "I can type this immediately")
}

func TestJobsListing(t *testing.T) {
	t.Parallel()
	t.Run("one job then more", func(t *testing.T) {
		t.Parallel()
		s := startShell(t, t.TempDir())
		s.startJob("sleep 10 &", 1)
		s.expect("jobs", "[1]+  Running                 sleep 10 &")
		s.startJob("sleep 20&", 2) // listed with a space before the &, as in bash
		s.expect("jobs",
			"[1]-  Running                 sleep 10 &",
			"[2]+  Running                 sleep 20 &")
		s.startJob("sleep '30' > /dev/null &", 3) // listed as typed
		want := []string{
			"[1]   Running                 sleep 10 &",
			"[2]-  Running                 sleep 20 &",
			"[3]+  Running                 sleep '30' > /dev/null &",
		}
		s.expect("jobs", want...)
		s.expect("jobs > jobs.txt")
		s.expect("cat jobs.txt", want...)
	})
	t.Run("the tester's three jobs", func(t *testing.T) {
		t.Parallel()
		s := startShell(t, t.TempDir())
		s.startJob("sleep 10 & ", 1)
		s.expect("jobs", "[1]+  Running                 sleep 10 &")
		s.startJob("sleep 20 & ", 2)
		s.expect("jobs",
			"[1]-  Running                 sleep 10 &",
			"[2]+  Running                 sleep 20 &")
		s.startJob("sleep 30 &", 3)
		s.expect("jobs",
			"[1]   Running                 sleep 10 &",
			"[2]-  Running                 sleep 20 &",
			"[3]+  Running                 sleep 30 &")
	})
}

func TestJobsReaping(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fifo := mkfifo(t, dir, "fifo")
	s := startShell(t, dir)
	cat := s.startJob("cat "+fifo+" &", 1)
	s.expect("jobs", listed("[1]+", "Running", "cat "+fifo+" &"))
	writeFIFO(t, fifo, "") // cat reads the end of its input and exits
	waitGone(t, cat)
	s.expect("jobs", listed("[1]+", "Done", "cat "+fifo)) // listed once, without the &
	s.expect("jobs")
	short := s.startJob("sleep 0.2 &", 1) // the table is empty again
	waitGone(t, short)
	s.expect("jobs", "[1]+  Done                    sleep 0.2")
}

func TestJobsReapingSeveral(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fifo1, fifo2 := mkfifo(t, dir, "fifo1"), mkfifo(t, dir, "fifo2")
	s := startShell(t, dir)
	s.startJob("sleep 500 &", 1)
	cat1 := s.startJob("cat "+fifo1+" &", 2)
	cat2 := s.startJob("cat "+fifo2+" &", 3)
	writeFIFO(t, fifo1, "")
	waitGone(t, cat1)
	s.expect("jobs",
		"[1]   Running                 sleep 500 &",
		listed("[2]-", "Done", "cat "+fifo1),
		listed("[3]+", "Running", "cat "+fifo2+" &"))
	writeFIFO(t, fifo2, "")
	waitGone(t, cat2)
	s.expect("jobs", "[1]-  Running                 sleep 500 &", listed("[3]+", "Done", "cat "+fifo2))
	s.expect("jobs", "[1]+  Running                 sleep 500 &")
}

func TestReapingBeforePrompt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fifo := mkfifo(t, dir, "fifo")
	s := startShell(t, dir)
	s.startJob("sleep 500 &", 1)
	cat := s.startJob("cat "+fifo+" &", 2)
	s.expect("jobs", "[1]-  Running                 sleep 500 &", listed("[2]+", "Running", "cat "+fifo+" &"))
	writeFIFO(t, fifo, "")
	waitGone(t, cat)
	s.expect("echo banana", "banana", listed("[2]+", "Done", "cat "+fifo))
	s.expect("jobs", "[1]+  Running                 sleep 500 &")

	// After an empty line.
	short := s.startJob("sleep 0.2 &", 2)
	waitGone(t, short)
	s.expect("", "[2]+  Done                    sleep 0.2")

	// After starting another job. The stages mark jobs by position, so the
	// ended job gets -; bash would leave it blank, as it gives - only to a job
	// still running when a newer one starts.
	short = s.startJob("sleep 0.2 &", 2)
	waitGone(t, short)
	if lines := s.run("sleep 30 &"); len(lines) != 2 || !strings.HasPrefix(lines[0], "[3] ") ||
		!jobStart.MatchString(lines[0]) || lines[1] != "[2]-  Done                    sleep 0.2" {
		t.Errorf("sleep 30 & printed %q; want job 3 started, then job 2 done", lines)
	}

	// After a program in the foreground, not while it runs.
	s.startJob("sleep 0.2 &", 4)
	s.expect("sleep 1", "[4]+  Done                    sleep 0.2")

	// After Ctrl-C drops a line.
	short = s.startJob("sleep 0.2 &", 4)
	waitGone(t, short)
	s.send("abc")
	sameLines(t, "Ctrl-C", s.enter("\x03"), "[4]+  Done                    sleep 0.2")

	// Each Done line came just once.
	s.expect("jobs", "[1]-  Running                 sleep 500 &", "[3]+  Running                 sleep 30 &")
}

func TestJobNumberRecycling(t *testing.T) {
	t.Parallel()
	t.Run("an empty table starts again at 1", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		fifo := mkfifo(t, dir, "fifo")
		s := startShell(t, dir)
		cat := s.startJob("cat "+fifo+" &", 1)
		writeFIFO(t, fifo, "")
		waitGone(t, cat)
		s.expect("echo apple", "apple", listed("[1]+", "Done", "cat "+fifo))
		s.startJob("sleep 100 &", 1)
		s.expect("jobs", "[1]+  Running                 sleep 100 &")
	})
	t.Run("an ended job's number is used again", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		fifo := mkfifo(t, dir, "fifo")
		s := startShell(t, dir)
		s.startJob("sleep 100 &", 1)
		cat := s.startJob("cat "+fifo+" &", 2)
		writeFIFO(t, fifo, "")
		waitGone(t, cat)
		s.expect("echo word", "word", listed("[2]+", "Done", "cat "+fifo))
		s.startJob("sleep 50 &", 2)
		s.expect("jobs", "[1]-  Running                 sleep 100 &", "[2]+  Running                 sleep 50 &")
	})
	t.Run("two jobs end together", func(t *testing.T) {
		t.Parallel()
		s := startShell(t, t.TempDir())
		first := s.startJob("sleep 1 &", 1)
		second := s.startJob("sleep 2 &", 2)
		waitGone(t, first)
		waitGone(t, second)
		s.expect("", "[1]-  Done                    sleep 1", "[2]+  Done                    sleep 2")
		s.startJob("sleep 10 &", 1)
	})
}

// Pipelines.

func TestPipelineInTerminal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	file := filepath.Join(dir, "file-1")
	appendFile(t, file, "raspberry strawberry\npear mango\npineapple apple\n")
	s := startShell(t, dir)
	start := s.row()
	s.send("tail -f " + file + " | head -n 5\r")
	s.waitText("pineapple apple")
	appendFile(t, file, "This is line 4.\n")
	s.waitText("This is line 4.")
	appendFile(t, file, "This is line 5.\n")
	// GNU tail, as on the tester, exits as soon as head has. The BSD tail on
	// macOS only finds out when it next writes, which then fails.
	if !s.poll(2*time.Second, s.screen.atPrompt) {
		appendFile(t, file, "This is line 6.\n")
	}
	sameLines(t, "tail -f | head -n 5", s.waitPrompt(start, "tail -f | head -n 5"),
		"raspberry strawberry", "pear mango", "pineapple apple", "This is line 4.", "This is line 5.")

	// In the background, a pipeline is one job, known by its last program.
	pid := s.startJob("sleep 1 | sleep 2 &", 1)
	if _, command := parentAndCommand(t, pid); command != "sleep 2" {
		t.Errorf("the job's process ID is %q's; want the last program's", command)
	}
	s.expect("jobs", "[1]+  Running                 sleep 1 | sleep 2 &")
	waitGone(t, pid)
	s.expect("", "[1]+  Done                    sleep 1 | sleep 2")
	s.expect("| wc", "syntax error near unexpected token `|'")
}

func TestBuiltinsInPipelines(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	appendFile(t, filepath.Join(dir, "listed-by-ls"), "")
	s := startShell(t, dir)
	if got := s.run("echo apple-orange | wc"); len(got) != 1 || !slices.Equal(strings.Fields(got[0]), []string{"1", "1", "13"}) {
		t.Errorf("echo apple-orange | wc printed %q", got)
	}
	s.expect("ls | type exit", "exit is a shell builtin")
	// These show the builtins run, not programs of the same names: macOS has
	// /usr/bin/jobs and /usr/bin/complete, which know nothing of the shell's.
	s.startJob("sleep 10 &", 1)
	s.expect("jobs | cat", "[1]+  Running                 sleep 10 &")
	s.expect("complete -C /bin/true git")
	s.expect("complete -p git | cat", "complete -C '/bin/true' git")
	s.expect("exit | cat") // as in bash, exit only ends its part of the pipeline
	s.expect("echo still here", "still here")
}

func TestLongerPipelines(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	foo := filepath.Join(dir, "foo")
	if err := os.Mkdir(foo, 0o755); err != nil {
		t.Fatal(err)
	}
	appendFile(t, filepath.Join(foo, "file"), "abc\nde\nfg\nhij\n")
	for _, name := range []string{"apple", "grape", "zebra"} {
		appendFile(t, filepath.Join(foo, name), name+"\n")
	}
	s := startShell(t, dir)
	if got := s.run("cat foo/file | head -n 3 | wc"); len(got) != 1 || !slices.Equal(strings.Fields(got[0]), []string{"3", "3", "10"}) {
		t.Errorf("cat foo/file | head -n 3 | wc printed %q", got)
	}
	// The long lines of ls -la depend on the system, so sh, run on the same
	// files, says what to expect.
	line := `ls -la foo | tail -n 5 | head -n 3 | grep "file"`
	sh := exec.Command("/bin/sh", "-c", line)
	sh.Dir, sh.Env = dir, shellEnv()
	out, err := sh.Output()
	if err != nil || !strings.Contains(string(out), "file") {
		t.Fatalf("sh -c %q: %v %q", line, err, out)
	}
	s.expect(line, strings.Split(strings.TrimSpace(string(out)), "\n")...)
}

// History.

func TestHistory(t *testing.T) {
	t.Parallel()
	t.Run("all of it", func(t *testing.T) {
		t.Parallel()
		s := startShell(t, t.TempDir())
		s.expect("echo hello", "hello")
		s.expect("echo world", "world")
		s.expect("invalid_command", "invalid_command: command not found")
		s.expect("history", // listing itself too, as the tester wants and zsh wouldn't
			"    1  echo hello",
			"    2  echo world",
			"    3  invalid_command",
			"    4  history")
		s.expect("type history", "history is a shell builtin")
		// Blank lines and lines dropped with Ctrl-C aren't kept, as in bash.
		s.expect("")
		s.send("echo dropped")
		s.interrupt()
		s.expect("history | tail -n 2", "    5  type history", "    6  history | tail -n 2")
	})
	t.Run("the last n", func(t *testing.T) {
		t.Parallel()
		s := startShell(t, t.TempDir())
		s.expect("echo hello", "hello")
		s.expect("echo world", "world")
		s.expect("invalid_command", "invalid_command: command not found")
		s.expect("history 2", "    3  invalid_command", "    4  history 2")
		s.expect("history 0")
		s.expect("history 100", // more than there are: all of them
			"    1  echo hello",
			"    2  echo world",
			"    3  invalid_command",
			"    4  history 2",
			"    5  history 0",
			"    6  history 100")
		s.expect("history abc", "history: abc: numeric argument required")
	})
	t.Run("read from a file", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		file := filepath.Join(dir, "history")
		appendFile(t, file, "echo hello\necho world\n\n") // as the tester writes it, ending in an empty line
		s := startShell(t, dir)
		s.expect("history -r " + file)
		s.expect("history",
			"    1  history -r "+file,
			"    2  echo hello",
			"    3  echo world",
			"    4  history")
		missing := filepath.Join(dir, "missing")
		s.expect("history -r " + missing) // as in bash, it adds nothing, without a word
		s.expect("history -r", "history: HISTFILE: parameter null or not set")
		s.expect("history 3",
			"    5  history -r "+missing,
			"    6  history -r",
			"    7  history 3")
	})
	t.Run("write to a file", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		file := filepath.Join(dir, "history") // not there yet
		s := startShell(t, dir)
		s.expect("echo hello", "hello")
		s.expect("echo world", "world")
		s.expect("history -w " + file)
		want := "echo hello\necho world\nhistory -w " + file + "\n"
		fileHolds(t, file, want)
		ownerOnly(t, file)
		s.expect("history -w " + file) // writing again replaces what the file held
		want += "history -w " + file + "\n"
		fileHolds(t, file, want)
		s.expect("history -w " + filepath.Join(dir, "no", "such", "file")) // as in bash, without a word
		// What history -w writes, history -r reads back.
		fresh := startShell(t, dir)
		fresh.expect("history -r " + file)
		fresh.expect("history 3", "    4  history -w "+file, "    5  history -w "+file, "    6  history 3")
	})
	t.Run("append to a file", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		file := filepath.Join(dir, "history")
		appendFile(t, file, "echo initial_command_1\necho initial_command_2\n") // as the tester writes it
		s := startShell(t, dir, "HISTFILE="+file)
		s.expect("echo new_command", "new_command")
		s.expect("history -a " + file)
		want := "echo initial_command_1\necho initial_command_2\necho new_command\nhistory -a " + file + "\n"
		fileHolds(t, file, want)
		// Again, it appends just the lines entered since it last ran, here to
		// $HISTFILE, as no file is named.
		s.expect("echo again", "again")
		s.expect("history -a")
		want += "echo again\nhistory -a\n"
		fileHolds(t, file, want)
		// It appends the commands run since it last ran, as the stage says, so
		// not lines history -r reads. Bash would append "echo read" in place of
		// "history -r", taking as many lines from the end of history as were
		// entered. As in bash, -w changes nothing for -a.
		other := filepath.Join(dir, "other")
		appendFile(t, other, "echo read\n")
		s.expect("history -r " + other)
		s.expect("history -w " + other)
		s.expect("history -a")
		want += "history -r " + other + "\nhistory -w " + other + "\nhistory -a\n"
		fileHolds(t, file, want)
	})
	t.Run("append errors", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		file := filepath.Join(dir, "history") // not there yet
		s := startShell(t, dir)
		// As in bash, it says when there's no file, or when it can't create one,
		// and then the lines wait for the next history -a.
		s.expect("history -a", "history: HISTFILE: parameter null or not set")
		missing := filepath.Join(dir, "no", "such", "file")
		s.expect("history -a "+missing, "history: "+missing+": cannot create: No such file or directory")
		s.expect("history -a " + file)
		want := "history -a\nhistory -a " + missing + "\nhistory -a " + file + "\n"
		fileHolds(t, file, want)
		ownerOnly(t, file)
		// A file that's there but can't be written to, such as a directory, is
		// passed over without a word, and the lines with it, as in bash.
		s.expect("history -a " + dir)
		s.expect("history -a " + file)
		fileHolds(t, file, want+"history -a "+file+"\n")
	})
	t.Run("the Up arrow", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		file := filepath.Join(dir, "history")
		appendFile(t, file, "echo hello\necho world\n")
		s := startShell(t, dir)
		s.expect("echo typed", "typed")
		s.expect("history -r " + file)
		// Up steps back through lines read in and lines typed alike, as in
		// bash, and Down forward again.
		const up, down = "\x1b[A", "\x1b[B"
		s.edit(up, "$ echo world")
		s.edit(up, "$ echo hello")
		s.edit(up, "$ history -r "+file)
		s.edit(up, "$ echo typed")
		s.edit(down, "$ history -r "+file)
		s.edit(down, "$ echo hello")
		sameLines(t, "Enter on a recalled line", s.enter("\r"), "hello")
		s.expect("history",
			"    1  echo typed",
			"    2  history -r "+file,
			"    3  echo hello",
			"    4  echo world",
			"    5  echo hello",
			"    6  history")
	})
	t.Run("read at startup", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		file := filepath.Join(dir, "history")
		appendFile(t, file, "echo hello\necho world\n") // as the tester writes it
		s := startShell(t, dir, "HISTFILE="+file)
		s.expect("history", "    1  echo hello", "    2  echo world", "    3  history")
		// As in bash, the Up arrow reaches the lines read, and history -a
		// doesn't append them, as they weren't entered.
		const up = "\x1b[A"
		s.edit(up, "$ history")
		s.edit(up, "$ echo world")
		s.edit(up, "$ echo hello")
		s.interrupt()
		other := filepath.Join(dir, "other")
		s.expect("history -a " + other)
		fileHolds(t, other, "history\nhistory -a "+other+"\n")
		// As in bash, history starts out empty when there's no such file, and a
		// last line without a newline is left out, at startup and by history -r.
		missing := startShell(t, dir, "HISTFILE="+filepath.Join(dir, "missing"))
		missing.expect("history", "    1  history")
		unfinished := filepath.Join(dir, "unfinished")
		appendFile(t, unfinished, "echo a\necho b")
		cut := startShell(t, dir, "HISTFILE="+unfinished)
		cut.expect("history", "    1  echo a", "    2  history")
		cut.expect("history -r " + unfinished)
		cut.expect("history 2", "    4  echo a", "    5  history 2")
	})
	t.Run("write on exit", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		file := filepath.Join(dir, "history") // not there yet
		s := startShell(t, dir, "HISTFILE="+file)
		s.expect("echo hello", "hello")
		s.expect("echo world", "world")
		s.exit("exit\r")
		fileHolds(t, file, "echo hello\necho world\nexit\n")
		ownerOnly(t, file)
		// When it can't write the file, it exits all the same, as bash does.
		lost := startShell(t, dir, "HISTFILE="+filepath.Join(dir, "no", "such", "file"))
		lost.expect("echo lost", "lost")
		lost.exit("exit\r")
	})
	t.Run("append on exit", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		file := filepath.Join(dir, "history")
		appendFile(t, file, "echo initial_command_1\necho initial_command_2\n") // as the tester writes it
		s := startShell(t, dir, "HISTFILE="+file)
		s.expect("echo new_command", "new_command")
		s.exit("exit\r")
		want := "echo initial_command_1\necho initial_command_2\necho new_command\nexit\n"
		fileHolds(t, file, want)
		// As in bash, it appends the lines entered since history -a last ran,
		// rather than writing the file anew: the next shell reads the lines in
		// but adds just its own, and lines added to the file meanwhile stay.
		// Ctrl-D on an empty line ends the shell as exit does, as in bash,
		// without being a line itself.
		next := startShell(t, dir, "HISTFILE="+file)
		next.expect("echo saved", "saved")
		next.expect("history -a")
		appendFile(t, file, "echo from elsewhere\n")
		next.expect("echo new", "new")
		next.exit("\x04")
		want += "echo saved\nhistory -a\necho from elsewhere\necho new\n"
		fileHolds(t, file, want)
	})
}

// Shell variables.

func TestDeclare(t *testing.T) {
	t.Parallel()
	s := startShell(t, t.TempDir())
	s.expect("type declare", "declare is a shell builtin")
	s.expect("declare foo=bar")
	s.expect("declare -p foo", `declare -- foo="bar"`)
	s.expect("declare -p missing_variable", "declare: missing_variable: not found")
	s.expect("declare foo=bar2")
	s.expect("declare -p foo", `declare -- foo="bar2"`)
	// Declared without a value, a variable shows none, as in bash 5, where
	// bash 3.2 shows an empty one; and declaring one again that way keeps its
	// value.
	s.expect("declare fresh foo")
	s.expect("declare -p fresh foo", "declare -- fresh", `declare -- foo="bar2"`)
	// A name starts with a letter or underscore, then has letters, digits and
	// underscores; for any other, no variable is made.
	s.expect("declare 23=x", "declare: `23=x': not a valid identifier")
	s.expect("declare -p 23", "declare: 23: not found")
	s.expect("declare _FOO=bar")
	s.expect("declare -p _FOO", `declare -- _FOO="bar"`)
}

func TestVariableExpansion(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Like the tester's program, it reports the arguments it's given.
	writeScript(t, dir, "custom_exe_1234", `echo "Program was passed $(($# + 1)) args (including program name)."
echo "Arg #0 (program name): ${0##*/}"
i=1
for arg do
	echo "Arg #$i: $arg"
	i=$((i + 1))
done
`)
	s := startShell(t, dir, "PATH="+dir+":/usr/bin:/bin")
	s.expect("declare Variable_1=Value_1")
	s.expect("declare Variable_2=Value_2")
	s.expect("custom_exe_1234 $Variable_1 $Variable_2",
		"Program was passed 3 args (including program name).",
		"Arg #0 (program name): custom_exe_1234",
		"Arg #1: Value_1",
		"Arg #2: Value_2")
	s.expect("echo $Variable_1 $Variable_2", "Value_1 Value_2")
}

// The pseudo-terminal.

// openPTY opens a pseudo-terminal: control is the side the tests read and type
// on, like a terminal window, and term the side the shell runs on.
func openPTY() (control, term *os.File, err error) {
	control, err = os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	name, err := ptsName(control.Fd())
	if err == nil {
		term, err = os.OpenFile(name, os.O_RDWR|syscall.O_NOCTTY, 0)
	}
	if err == nil {
		size := [4]uint16{24, columns} // rows and columns, then pixel sizes nothing uses
		if err = ioctl(term.Fd(), syscall.TIOCSWINSZ, unsafe.Pointer(&size)); err != nil {
			term.Close()
		}
	}
	if err != nil {
		control.Close()
		return nil, nil, err
	}
	return control, term, nil
}

// ptsName unlocks the pseudo-terminal whose control side is fd and returns the
// path of its terminal side.
func ptsName(fd uintptr) (string, error) {
	switch runtime.GOOS {
	case "darwin":
		const grant, unlock, getName = 0x20007454, 0x20007452, 0x40807453 // TIOCPTYGRANT, TIOCPTYUNLK, TIOCPTYGNAME
		if err := ioctl(fd, grant, nil); err != nil {
			return "", err
		}
		if err := ioctl(fd, unlock, nil); err != nil {
			return "", err
		}
		var name [128]byte
		if err := ioctl(fd, getName, unsafe.Pointer(&name)); err != nil {
			return "", err
		}
		return string(bytes.TrimRight(name[:], "\x00")), nil
	case "linux":
		const unlock, getNumber = 0x40045431, 0x80045430 // TIOCSPTLCK, TIOCGPTN
		var locked int32
		if err := ioctl(fd, unlock, unsafe.Pointer(&locked)); err != nil {
			return "", err
		}
		var n uint32
		if err := ioctl(fd, getNumber, unsafe.Pointer(&n)); err != nil {
			return "", err
		}
		return "/dev/pts/" + strconv.Itoa(int(n)), nil
	}
	return "", errors.New("the tests can't open a pseudo-terminal on " + runtime.GOOS)
}

func ioctl(fd, request uintptr, arg unsafe.Pointer) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, uintptr(arg)); errno != 0 {
		return errno
	}
	return nil
}

// screen is just enough of a terminal for what the shell writes: text, carriage
// returns, newlines, backspaces and bells, and the escape sequences readline
// clears and moves with.
type screen struct {
	rows     [][]byte
	row, col int
	bells    int
	partial  []byte // an escape sequence cut off at the end of a read
}

func (s *screen) write(p []byte) {
	p = append(s.partial, p...)
	s.partial = nil
	for i := 0; i < len(p); i++ {
		switch c := p[i]; {
		case c == 0x1b: // ESC [, parameters and a final byte; or ESC and one byte
			end := i + 1
			if end < len(p) && p[end] == '[' {
				for end++; end < len(p) && (p[end] < 0x40 || p[end] > 0x7e); end++ {
				}
			}
			if end >= len(p) {
				s.partial = p[i:]
				return
			}
			if p[i+1] == '[' {
				s.csi(string(p[i+2:end]), p[end])
			}
			i = end
		case c == '\a':
			s.bells++
		case c == '\r':
			s.col = 0
		case c == '\n':
			s.row++
		case c == '\b':
			s.col = max(s.col-1, 0)
		case c >= ' ':
			if s.col >= columns {
				s.row, s.col = s.row+1, 0
			}
			line := s.line()
			for len(*line) <= s.col {
				*line = append(*line, ' ')
			}
			(*line)[s.col] = c
			s.col++
		}
	}
}

// csi applies the escape sequence ESC [ params final.
func (s *screen) csi(params string, final byte) {
	n, _ := strconv.Atoi(strings.TrimPrefix(params, "?"))
	switch final {
	case 'J': // erase to the end of the screen
		if n == 0 {
			s.eraseRight()
			s.rows = s.rows[:min(s.row+1, len(s.rows))]
		}
	case 'K': // erase to the end of the line, or all of it
		if n == 0 {
			s.eraseRight()
		} else if n == 2 {
			*s.line() = nil
		}
	case 'A':
		s.row = max(s.row-max(n, 1), 0)
	case 'B':
		s.row += max(n, 1)
	case 'C':
		s.col += max(n, 1)
	case 'D':
		s.col = max(s.col-max(n, 1), 0)
	case 'G':
		s.col = max(n, 1) - 1
	}
}

// line returns the cursor's row, adding rows up to it as needed.
func (s *screen) line() *[]byte {
	for len(s.rows) <= s.row {
		s.rows = append(s.rows, nil)
	}
	return &s.rows[s.row]
}

func (s *screen) eraseRight() {
	if line := s.line(); s.col < len(*line) {
		*line = (*line)[:s.col]
	}
}

// text returns row r without trailing spaces.
func (s *screen) text(r int) string {
	if r < 0 || r >= len(s.rows) {
		return ""
	}
	return strings.TrimRight(string(s.rows[r]), " ")
}

// cursorLine returns the cursor's row up to the cursor: the prompt and the line
// being edited.
func (s *screen) cursorLine() string {
	line := *s.line()
	return string(line[:min(s.col, len(line))])
}

// atPrompt reports whether the cursor is just after a fresh prompt.
func (s *screen) atPrompt() bool {
	return s.cursorLine() == "$ " && s.text(s.row) == "$"
}

// texts returns the rows from up to but not including to, leaving out blank ones.
func (s *screen) texts(from, to int) []string {
	var lines []string
	for r := from; r < to; r++ {
		if text := s.text(r); text != "" {
			lines = append(lines, text)
		}
	}
	return lines
}

// session is the shell running on a pseudo-terminal, and what it has shown.
type session struct {
	t       *testing.T
	cmd     *exec.Cmd
	control *os.File
	closed  chan struct{} // closed once nothing has the terminal open anymore

	mu     sync.Mutex
	raw    []byte // everything written to the terminal
	screen screen
}

// startShell starts the shell in dir, with env added to its environment, and
// waits for its first prompt. The shell exits when the test ends.
func startShell(t *testing.T, dir string, env ...string) *session {
	t.Helper()
	control, term, err := openPTY()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(shellPath)
	cmd.Dir, cmd.Env = dir, shellEnv(env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = term, term, term
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true} // its controlling terminal
	err = cmd.Start()
	term.Close()
	if err != nil {
		control.Close()
		t.Fatal(err)
	}
	s := &session{t: t, cmd: cmd, control: control, closed: make(chan struct{})}
	go s.read()
	t.Cleanup(s.close)
	s.waitFor("the first prompt", s.screen.atPrompt)
	return s
}

// read records what's written to the terminal until it closes. It has to keep
// reading: on macOS, a shell whose output nobody reads can hang while exiting.
func (s *session) read() {
	defer close(s.closed)
	buf := make([]byte, 4096)
	for {
		n, err := s.control.Read(buf)
		s.mu.Lock()
		s.raw = append(s.raw, buf[:n]...)
		s.screen.write(buf[:n])
		s.mu.Unlock()
		if err != nil {
			return
		}
	}
}

// close ends the shell with exit, unless the test has, then any programs it
// left running, which would keep the terminal open, and checks that the race
// detector kept quiet.
func (s *session) close() {
	select {
	case <-s.closed: // nothing would read it
	default:
		_, _ = s.control.WriteString("exit\r")
	}
	exited := make(chan struct{})
	go func() {
		_ = s.cmd.Wait()
		close(exited)
	}()
	select {
	case <-exited:
	case <-time.After(timeout):
		s.t.Error("the shell didn't exit")
	}
	_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL) // the shell leads a process group its jobs are in
	<-exited
	select {
	case <-s.closed:
	case <-time.After(timeout):
		s.t.Error("the terminal didn't close")
	}
	_ = s.control.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	if bytes.Contains(s.raw, []byte("WARNING: DATA RACE")) {
		s.t.Errorf("the race detector reported a race:\n%s", s.raw)
	}
}

// send types keys.
func (s *session) send(keys string) {
	s.t.Helper()
	if _, err := s.control.WriteString(keys); err != nil {
		s.t.Fatal(err)
	}
}

// poll reports whether cond, checked with the screen locked, holds within d.
func (s *session) poll(d time.Duration, cond func() bool) bool {
	for deadline := time.Now().Add(d); ; time.Sleep(10 * time.Millisecond) {
		s.mu.Lock()
		ok := cond()
		s.mu.Unlock()
		if ok || time.Now().After(deadline) {
			return ok
		}
	}
}

// waitFor waits until cond holds, and fails the test if it doesn't in time.
func (s *session) waitFor(what string, cond func() bool) {
	s.t.Helper()
	if !s.poll(timeout, cond) {
		s.t.Fatalf("timed out waiting for %s; the screen reads:\n%s", what, s.dump())
	}
}

// dump returns the screen's rows, for a failure message.
func (s *session) dump() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	for r := range s.screen.rows {
		fmt.Fprintf(&b, "\t%s\n", s.screen.text(r))
	}
	return b.String()
}

// row returns the cursor's row.
func (s *session) row() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.screen.row
}

// waitText waits until a row of the screen shows text.
func (s *session) waitText(text string) {
	s.t.Helper()
	s.waitFor(strconv.Quote(text), func() bool {
		for r := range s.screen.rows {
			if strings.Contains(s.screen.text(r), text) {
				return true
			}
		}
		return false
	})
}

// waitPrompt waits for a prompt below row start, after what, and returns the
// nonblank lines between them.
func (s *session) waitPrompt(start int, what string) []string {
	s.t.Helper()
	var lines []string
	s.waitFor("a prompt after "+strconv.Quote(what), func() bool {
		if s.screen.row <= start || !s.screen.atPrompt() {
			return false
		}
		lines = s.screen.texts(start+1, s.screen.row)
		return true
	})
	return lines
}

// enter types keys that end the line being edited, such as a command and
// Enter, and returns the nonblank lines shown before the next prompt.
func (s *session) enter(keys string) []string {
	s.t.Helper()
	start := s.row()
	s.send(keys)
	return s.waitPrompt(start, keys)
}

// run runs a command line and returns the nonblank lines it shows.
func (s *session) run(line string) []string {
	s.t.Helper()
	return s.enter(line + "\r")
}

// expect runs a command line and fails the test unless it shows want.
func (s *session) expect(line string, want ...string) {
	s.t.Helper()
	sameLines(s.t, strconv.Quote(line), s.run(line), want...)
}

// interrupt drops the line being edited with Ctrl-C and checks nothing shows.
func (s *session) interrupt() {
	s.t.Helper()
	sameLines(s.t, "Ctrl-C", s.enter("\x03"))
}

// exit types keys that end the shell, such as exit and Enter, and waits until
// it has closed the terminal. It checks that the line then reads "$ exit", as
// bash shows Ctrl-D too, that nothing showed after it, and that the shell
// exited with status 0.
func (s *session) exit(keys string) {
	s.t.Helper()
	start := s.row()
	s.send(keys)
	select {
	case <-s.closed:
	case <-time.After(timeout):
		s.t.Fatalf("the shell didn't exit; the screen reads:\n%s", s.dump())
	}
	s.mu.Lock()
	lines := s.screen.texts(start, len(s.screen.rows))
	s.mu.Unlock()
	sameLines(s.t, strconv.Quote(keys), lines, "$ exit")
	if err := s.cmd.Wait(); err != nil {
		s.t.Errorf("the shell exited with %v", err)
	}
}

// tab types keys, ending in a Tab, and fails the test unless the line being
// edited comes to read want, with or without the bell as bell says.
func (s *session) tab(keys, want string, bell bool) {
	s.t.Helper()
	s.mu.Lock()
	bells := s.screen.bells
	s.mu.Unlock()
	rang := func() bool { return s.screen.bells > bells }
	s.send(keys)
	if bell {
		s.waitFor("the bell after "+strconv.Quote(keys), rang)
	}
	s.waitFor("the line to read "+strconv.Quote(want), func() bool { return s.screen.cursorLine() == want })
	if !bell && s.poll(300*time.Millisecond, rang) {
		s.t.Errorf("the bell rang after %q", keys)
	}
}

// edit types keys that change the line being edited, such as the Up arrow,
// and waits until the line reads want.
func (s *session) edit(keys, want string) {
	s.t.Helper()
	s.send(keys)
	s.waitFor("the line to read "+strconv.Quote(want), func() bool { return s.screen.cursorLine() == want })
}

// jobStart matches the line the shell shows when a job starts.
var jobStart = regexp.MustCompile(`^\[(\d+)\] (\d+)$`)

// startJob runs a command line ending in &, checks that it starts job number
// and shows nothing else, and returns the job's process ID.
func (s *session) startJob(line string, number int) (pid int) {
	s.t.Helper()
	lines := s.run(line)
	if len(lines) != 1 || jobStart.FindStringSubmatch(lines[0]) == nil {
		s.t.Fatalf("%q printed %q; want a job started", line, lines)
	}
	m := jobStart.FindStringSubmatch(lines[0])
	if m[1] != strconv.Itoa(number) {
		s.t.Errorf("%q started job %s; want job %d", line, m[1], number)
	}
	pid, _ = strconv.Atoi(m[2])
	return pid
}

// sameLines fails the test unless the lines what showed, got, are want.
func sameLines(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s showed\n\t%s\nwant\n\t%s", what, strings.Join(got, "\n\t"), strings.Join(want, "\n\t"))
	}
}

// listed returns a line of the jobs listing as the stages specify it: the job
// number and its marker, two spaces, the status padded to 24 columns, and the
// command line.
func listed(numberAndMarker, status, command string) string {
	return fmt.Sprintf("%s  %-24s%s", numberAndMarker, status, command)
}

// Processes and files.

// waitGone waits until process pid has exited and the shell has reaped it.
func waitGone(t *testing.T, pid int) {
	t.Helper()
	// Signal 0 only checks that the process is there, which it still is as a
	// zombie until the shell reaps it.
	for deadline := time.Now().Add(timeout); syscall.Kill(pid, 0) == nil; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("process %d is still there", pid)
		}
	}
	time.Sleep(50 * time.Millisecond) // for the shell to note its job is done
}

// parentAndCommand returns process pid's parent's process ID and its command line.
func parentAndCommand(t *testing.T, pid int) (int, string) {
	t.Helper()
	out, err := exec.Command("ps", "-o", "ppid=,command=", "-p", strconv.Itoa(pid)).Output()
	ppid, command, _ := strings.Cut(strings.TrimSpace(string(out)), " ")
	parent, convErr := strconv.Atoi(ppid)
	if err != nil || convErr != nil {
		t.Fatalf("ps -p %d: %v %q", pid, err, out)
	}
	return parent, strings.TrimSpace(command)
}

// waitChild waits for a child of process parent other than except and returns
// its process ID, or 0 if pgrep isn't there to find it.
func waitChild(t *testing.T, parent, except int) int {
	t.Helper()
	if _, err := exec.LookPath("pgrep"); err != nil {
		return 0
	}
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		out, _ := exec.Command("pgrep", "-P", strconv.Itoa(parent)).Output()
		for _, field := range strings.Fields(string(out)) {
			if pid, err := strconv.Atoi(field); err == nil && pid != except {
				return pid
			}
		}
	}
	t.Fatalf("process %d started no other child", parent)
	return 0
}

// stdio returns what process pid has open as its standard input, output and
// error, or nil if the system can't say.
func stdio(pid int) []string {
	names := make([]string, 3)
	if _, err := os.Stat("/proc/self/fd"); err == nil {
		for fd := range names {
			names[fd], _ = os.Readlink(fmt.Sprintf("/proc/%d/fd/%d", pid, fd))
		}
		return names
	}
	out, err := exec.Command("lsof", "-a", "-p", strconv.Itoa(pid), "-d", "0,1,2", "-F", "fn").Output()
	if err != nil {
		return nil
	}
	fd := -1
	for _, line := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(line, "f"); ok {
			fd, _ = strconv.Atoi(rest)
		} else if rest, ok := strings.CutPrefix(line, "n"); ok && fd >= 0 && fd < len(names) {
			names[fd] = rest
		}
	}
	return names
}

// mkfifo makes a FIFO named name in dir and returns its path.
func mkfifo(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeFIFO writes text to the FIFO at path, from the test's own process as the
// tester does, once something has it open to read; then the reader sees the
// end of its input.
func writeFIFO(t *testing.T, path, text string) {
	t.Helper()
	for deadline := time.Now().Add(timeout); ; time.Sleep(10 * time.Millisecond) {
		f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			_, err = f.WriteString(text)
			// On macOS, a reader still in open can miss a writer that comes and
			// goes, and then wait for another forever, so this one stays a
			// moment for it to finish opening.
			time.Sleep(100 * time.Millisecond)
			if closeErr := f.Close(); err == nil {
				err = closeErr
			}
			if err != nil {
				t.Fatal(err)
			}
			return
		}
		if !errors.Is(err, syscall.ENXIO) || time.Now().After(deadline) { // ENXIO: no reader yet
			t.Fatal(err)
		}
	}
}

// appendFile adds text to the end of the file at path, creating it if needed.
func appendFile(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err == nil {
		_, err = f.WriteString(text)
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}
	if err != nil {
		t.Fatal(err)
	}
}

// fileHolds fails the test unless the file at path holds want.
func fileHolds(t *testing.T, path, want string) {
	t.Helper()
	if got, err := os.ReadFile(path); err != nil || string(got) != want {
		t.Errorf("%s holds %q (%v); want %q", path, got, err, want)
	}
}

// ownerOnly fails the test unless the file at path is readable only by its
// owner, as bash makes history files.
func ownerOnly(t *testing.T, path string) {
	t.Helper()
	if info, err := os.Stat(path); err != nil {
		t.Error(err)
	} else if info.Mode().Perm() != 0o600 {
		t.Errorf("%s is %v; want it readable only by its owner, as bash makes history files", path, info.Mode())
	}
}

// writeScript writes a shell script with body to dir/name and returns its path.
func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
