// Package cli implements the pagerelic command line.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// Exit codes (see docs/operations.md).
const (
	ExitOK          = 0
	ExitFailure     = 1
	ExitUsage       = 2
	ExitPartial     = 3 // completed, but found corruption or undecodable data
	ExitInterrupted = 130
)

type app struct {
	ctx    context.Context
	stdout io.Writer
	stderr io.Writer
	log    *slog.Logger
	args   []string
}

type command struct {
	name, summary string
	run           func(*app, []string) int
}

func commands() []command {
	return []command{
		{"inspect", "show page headers, checksums and line pointers of a relation file", runInspect},
		{"verify", "verify checksums and page structure of files or a whole data directory", runVerify},
		{"rows", "recover rows (live, deleted, updated, aborted, remnants) from a heap file", runRows},
		{"relations", "list databases, or relations and their columns from the catalogs (incl. dropped)", runRelations},
		{"carve", "find PostgreSQL pages on a raw disk or image and group them by relation", runCarve},
		{"xact", "look up transaction status in pg_xact", runXact},
		{"version", "print build information", runVersion},
	}
}

// Run executes a command line and returns the exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	a := &app{ctx: ctx, stdout: stdout, stderr: stderr, args: args}
	if len(args) < 2 || args[1] == "-h" || args[1] == "--help" || args[1] == "help" {
		a.usage()
		if len(args) < 2 {
			return ExitUsage
		}
		return ExitOK
	}
	for _, c := range commands() {
		if c.name == args[1] {
			return c.run(a, args[2:])
		}
	}
	fmt.Fprintf(stderr, "pagerelic: unknown command %q\n\n", args[1])
	a.usage()
	return ExitUsage
}

func (a *app) usage() {
	fmt.Fprintln(a.stderr, `pagerelic — PostgreSQL page-level recovery and forensic analysis

Usage: pagerelic <command> [flags] [args]

Works offline on PostgreSQL files, never through a server, and never
writes to its inputs. Stop PostgreSQL (or work on a copy/image) first.

Commands:`)
	for _, c := range commands() {
		fmt.Fprintf(a.stderr, "  %-10s %s\n", c.name, c.summary)
	}
	fmt.Fprintln(a.stderr, `
Run "pagerelic <command> -h" for flags.
Exit codes: 0 ok, 1 failure, 2 usage, 3 corruption/undecodable data found, 130 interrupted.`)
}

type common struct{ logLevel, logFormat string }

func (c *common) register(fs *flag.FlagSet) {
	fs.StringVar(&c.logLevel, "log-level", "info", "log level: debug, info, warn, error")
	fs.StringVar(&c.logFormat, "log-format", "text", "log format on stderr: text or json")
}

// parse accepts flags before or after positional arguments.
func (a *app) parse(fs *flag.FlagSet, c *common, args []string, positional string) ([]string, bool) {
	fs.SetOutput(a.stderr)
	fs.Usage = func() {
		fmt.Fprintf(a.stderr, "Usage: pagerelic %s [flags] %s\n\nFlags:\n", fs.Name(), positional)
		fs.PrintDefaults()
	}
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			flags = append(flags, arg)
			name := strings.TrimLeft(arg, "-")
			if !strings.Contains(name, "=") {
				if f := fs.Lookup(name); f != nil {
					if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); !ok || !bf.IsBoolFlag() {
						if i+1 < len(args) {
							flags = append(flags, args[i+1])
							i++
						}
					}
				}
			}
			continue
		}
		pos = append(pos, arg)
	}
	if err := fs.Parse(flags); err != nil {
		return nil, false
	}
	lvl := slog.LevelInfo
	if c != nil {
		if err := lvl.UnmarshalText([]byte(c.logLevel)); err != nil {
			fmt.Fprintln(a.stderr, "pagerelic: bad --log-level")
			return nil, false
		}
	}
	opts := &slog.HandlerOptions{Level: lvl}
	if c != nil && c.logFormat == "json" {
		a.log = slog.New(slog.NewJSONHandler(a.stderr, opts))
	} else {
		a.log = slog.New(slog.NewTextHandler(a.stderr, opts))
	}
	return pos, true
}

func (a *app) fail(err error) int {
	if errors.Is(err, context.Canceled) {
		a.log.Warn("interrupted")
		return ExitInterrupted
	}
	a.log.Error("failed", "err", err)
	return ExitFailure
}

func (a *app) usageErr(fs *flag.FlagSet, format string, args ...any) int {
	fmt.Fprintf(a.stderr, "pagerelic %s: %s\n\n", fs.Name(), fmt.Sprintf(format, args...))
	fs.Usage()
	return ExitUsage
}
