// Command outsource is the single binary behind this skill's tools.
//
// It is multi-call, the way busybox and git are: the command it runs comes
// from argv[0] when that names a known tool, and otherwise from the first
// argument. That is not a flourish — it is what lets every existing call
// site keep working during the port. Docs, hooks, installed skills, a user's
// local overlay and 135 test cases all invoke these tools by path
// (bin/runs.sh list), and a rename would break every one of them at once. So
// bin/runs.sh becomes a name for this binary rather than a different program,
// and the CLI surface does not move.
//
// Ported tools answer here; the rest are still shell scripts beside this
// binary, and this dispatcher says so out loud rather than failing obscurely.
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/midagedev/outsource/internal/cred"
	"github.com/midagedev/outsource/internal/guard"
	"github.com/midagedev/outsource/internal/launch"
	"github.com/midagedev/outsource/internal/overlays"
	"github.com/midagedev/outsource/internal/quota"
	"github.com/midagedev/outsource/internal/report"
	"github.com/midagedev/outsource/internal/runs"
	"github.com/midagedev/outsource/internal/speclint"
	"github.com/midagedev/outsource/internal/statusline"
	"github.com/midagedev/outsource/internal/telemetry"
)

// tool is one dispatchable command.
type tool struct {
	name string
	main func(args []string, stdin io.Reader, stdout, stderr io.Writer) int
}

// Tools that ignore stdin are adapted here rather than each growing a
// parameter it does not use.
func noStdin(f func(args []string, stdout, stderr io.Writer) int) func([]string, io.Reader, io.Writer, io.Writer) int {
	return func(args []string, _ io.Reader, stdout, stderr io.Writer) int {
		return f(args, stdout, stderr)
	}
}

var tools = []tool{
	{"credential", noStdin(cred.Main)},
	{"verify-key", func(a []string, in io.Reader, out, err io.Writer) int { return cred.VerifyMain(a, out, err, in) }},
	{"guard", guard.Main},
	{"grok-run", noStdin(launch.GrokMain)},
	{"wait", noStdin(launch.WaitMain)},
	{"outsource-run", noStdin(launch.OutsourceMain)},
	{"overlays", noStdin(overlays.Main)},
	{"last-report", noStdin(report.Main)},
	{"quota", noStdin(quota.Main)},
	{"runs", noStdin(runs.Main)},
	{"spec-lint", noStdin(speclint.Main)},
	{"statusline", statusline.Main},
	{"telemetry", noStdin(telemetry.ReportMain)},
}

// toolFor resolves a name to a tool. The `.sh` suffix is accepted because the
// legacy names carry it, and dropping it is the eventual rename.
func toolFor(name string) *tool {
	name = strings.TrimSuffix(filepath.Base(name), ".sh")
	for i := range tools {
		if tools[i].name == name {
			return &tools[i]
		}
	}
	return nil
}

func names() string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.name)
	}
	return strings.Join(out, "|")
}

// run is the single choke point every tool passes through, which is what makes
// telemetry a few lines instead of an instrumentation project: the exit code, the
// duration and the flag names are all visible here, and the tools only have to
// supply a reason when they know one.
func run(t *tool, args []string) {
	started := time.Now()
	rc := t.main(args, os.Stdin, os.Stdout, os.Stderr)
	telemetry.Record(t.name, args, rc, started)
	os.Exit(rc)
}

func main() {
	// argv[0] first: invoked through one of its tool names, every argument
	// belongs to that tool.
	if t := toolFor(os.Args[0]); t != nil {
		run(t, os.Args[1:])
	}
	// Otherwise the first argument selects the tool: `outsource runs list`.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "help", "-h", "--help":
			fmt.Printf("usage: outsource <%s> [args...]\n", names())
			os.Exit(0)
		}
		if t := toolFor(os.Args[1]); t != nil {
			run(t, os.Args[2:])
		}
		fmt.Fprintf(os.Stderr, "outsource: unknown tool: %s (have: %s)\n", os.Args[1], names())
		os.Exit(runs.ExitUsage)
	}
	fmt.Fprintf(os.Stderr, "usage: outsource <%s> [args...]\n", names())
	os.Exit(runs.ExitUsage)
}
