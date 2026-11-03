package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/attest-ai/attest/engine/internal/capture"
)

func init() {
	register(command{name: "replay", summary: "serve a fixture's recorded model responses to the agent (zero model calls)", run: handleReplayCommand})
}

const replayUsage = `usage: attest-engine replay (--name <name> | --fixture <file>) [options] -- <command> [args...]
       attest-engine replay (--name <name> | --fixture <file>) --serve [options]

Runs <command> with OPENAI_BASE_URL and ANTHROPIC_BASE_URL pointing at a
replay proxy on 127.0.0.1 that answers from the fixture's recorded model
exchanges. A request that matches no recording is a miss: the proxy answers it
with an error naming the first component that changed. Exits 0 when every
request was served, 1 when the command failed or any request missed.

options:
  --name <name>          fixture name, read from <fixtures-dir>/<name>.attest.json
  --fixtures-dir <dir>   fixture directory (default tests/attest/fixtures)
  --fixture <file>       fixture file
  --mode strict|auto     strict (default) fails a miss; auto forwards a miss
                         live when the request carries exactly the
                         OPENAI_API_KEY / ANTHROPIC_API_KEY of this
                         environment for that provider, and otherwise
                         reports it
  --no-live              never forward a miss live, whatever keys are set
                         (a miss is reported); used with the egress guard
  --world <world.yaml>   also host the world the agent acts on, starting from
                         its initial state; its base URL variable is set for
                         the command (or listed in the --serve env)
  --serve                for an SDK helper: print {"env":{...}} as one JSON
                         line, serve until stdin sends "stop" or closes, then
                         print {"summary":{...}} and exit`

type replayOptions struct {
	name, fixturesDir, fixture, mode string
	world                            string
	serve, noLive                    bool
	command                          []string
}

func handleReplayCommand(args []string) {
	os.Exit(runReplay(args, os.Stdin, os.Stdout, os.Stderr))
}

func parseReplayArgs(args []string, stderr io.Writer) (*replayOptions, bool) {
	o := &replayOptions{}
	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.name, "name", "", "")
	fs.StringVar(&o.fixturesDir, "fixtures-dir", filepath.Join("tests", "attest", "fixtures"), "")
	fs.StringVar(&o.fixture, "fixture", "", "")
	fs.StringVar(&o.mode, "mode", capture.ReplayStrict, "")
	fs.StringVar(&o.world, "world", "", "")
	fs.BoolVar(&o.serve, "serve", false, "")
	fs.BoolVar(&o.noLive, "no-live", false, "")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stderr, "replay: %s\n%s\n", terminalSafe(err.Error()), replayUsage)
		return nil, false
	}
	o.command = fs.Args()
	usage := func(msg string) (*replayOptions, bool) {
		fmt.Fprintf(stderr, "replay: %s\n%s\n", msg, replayUsage)
		return nil, false
	}
	switch {
	case (o.name == "") == (o.fixture == ""):
		return usage("give exactly one of --name or --fixture")
	case o.mode != capture.ReplayStrict && o.mode != capture.ReplayAuto:
		return usage(fmt.Sprintf("--mode %q: want strict or auto", terminalSafe(o.mode)))
	case o.serve == (len(o.command) > 0):
		return usage("give exactly one of --serve or -- <command>")
	}
	if o.name != "" {
		if err := capture.ValidateName(o.name); err != nil {
			return usage(terminalSafe(err.Error()))
		}
		o.fixture = filepath.Join(o.fixturesDir, capture.FixtureFile(o.name))
	}
	return o, true
}

func runReplay(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	o, ok := parseReplayArgs(args, stderr)
	if !ok {
		return 2
	}
	f, err := capture.LoadFixture(o.fixture)
	if err != nil {
		fmt.Fprintf(stderr, "replay: %s\n", terminalSafe(err.Error()))
		return 1
	}
	environ := os.Environ()
	var opts []capture.Option
	if o.world != "" {
		w, werr := loadWorld(o.world)
		if werr != nil {
			fmt.Fprintf(stderr, "replay: %s\n", terminalSafe(werr.Error()))
			return 2
		}
		opts = append(opts, capture.WithWorld(w))
	}
	s, err := capture.StartReplay(capture.ReplayOptions{
		Mode: o.mode, Fixture: f, Environ: environ, Upstreams: capture.UpstreamsFromEnv(environ), NoLive: o.noLive,
	}, opts...)
	if err != nil {
		fmt.Fprintf(stderr, "replay: %s\n", terminalSafe(err.Error()))
		return 1
	}
	if o.serve {
		return replayServe(s, stdin, stdout, stderr)
	}
	return replayCommand(s, o.command, environ, stderr)
}

// replayServe hosts the listener for an SDK helper: it prints the environment
// as one JSON line, waits for "stop" on stdin (or end of input), then prints
// the summary as one JSON line.
func replayServe(s *capture.Session, stdin io.Reader, stdout, stderr io.Writer) int {
	if err := writeJSONLine(stdout, map[string]any{"env": s.Env()}); err != nil {
		_ = s.Close()
		fmt.Fprintf(stderr, "replay: %s\n", terminalSafe(err.Error()))
		return 1
	}
	sc := bufio.NewScanner(stdin)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "stop" {
			break
		}
	}
	if err := s.Close(); err != nil {
		fmt.Fprintf(stderr, "replay: %s\n", terminalSafe(err.Error()))
		return 1
	}
	if err := writeJSONLine(stdout, map[string]any{"summary": s.ReplaySummary()}); err != nil {
		fmt.Fprintf(stderr, "replay: %s\n", terminalSafe(err.Error()))
		return 1
	}
	return 0
}

// replayCommand runs argv against the replay proxy and prints the summary.
func replayCommand(s *capture.Session, argv, environ []string, stderr io.Writer) int {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = withEnv(environ, s.Env())
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		_ = s.Close()
		fmt.Fprintf(stderr, "replay: running %s: %s\n", terminalSafe(argv[0]), terminalSafe(err.Error()))
		return 1
	}
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	stopForward := make(chan struct{})
	go func() {
		for {
			select {
			case sig := <-signals:
				_ = cmd.Process.Signal(sig)
			case <-stopForward:
				return
			}
		}
	}()
	runErr := cmd.Wait()
	signal.Stop(signals)
	close(stopForward)
	if err := s.Close(); err != nil {
		fmt.Fprintf(stderr, "replay: %s\n", terminalSafe(err.Error()))
		return 1
	}
	sum := s.ReplaySummary()
	writeReplaySummary(stderr, sum)
	if snap := s.WorldSnapshot(); snap != nil {
		writeWorldSummary(stderr, snap)
	}
	var exitErr *exec.ExitError
	switch {
	case errors.As(runErr, &exitErr):
		fmt.Fprintf(stderr, "replay: command exited %d\n", exitErr.ExitCode())
		return 1
	case runErr != nil:
		fmt.Fprintf(stderr, "replay: running %s: %s\n", terminalSafe(argv[0]), terminalSafe(runErr.Error()))
		return 1
	case sum.Misses > 0:
		return 1
	}
	return 0
}

// writeReplaySummary prints the one-line summary and each miss.
func writeReplaySummary(w io.Writer, sum capture.ReplaySummary) {
	fmt.Fprintf(w, "replay %s (%s): %d replayed · %s\n", terminalSafe(sum.Fixture), sum.Mode, sum.Replayed, liveLine(sum))
	for _, rec := range sum.Records {
		if rec.Outcome == capture.OutcomeMiss {
			fmt.Fprintf(w, "  %s\n", terminalSafe(rec.Message))
		}
	}
}

// liveLine is the "N live model calls · $X.XX" summary shared with the SDKs.
func liveLine(sum capture.ReplaySummary) string {
	line := fmt.Sprintf("%d live model %s · $%.2f", sum.Live, plural(sum.Live, "call", "calls"), sum.LiveCostUSD)
	if sum.UnpricedLiveCalls > 0 {
		line += fmt.Sprintf(" (+%d unpriced)", sum.UnpricedLiveCalls)
	}
	return line
}
