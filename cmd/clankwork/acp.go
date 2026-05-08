package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rot13maxi/clankwork/internal/config"
	"github.com/rot13maxi/clankwork/internal/runtimeenv"
	"github.com/rot13maxi/clankwork/internal/worker"
	"github.com/urfave/cli/v3"
)

type acpProbe struct {
	home        string
	runtimeName string
	runtime     config.RuntimeConfig
	cwd         string
	timeout     time.Duration
	sessionName string
	runtimeInst *worker.ACPRuntime
}

func acpCmd() *cli.Command {
	return &cli.Command{
		Name:  "acp",
		Usage: "Inspect and test ACP agent adapter capabilities",
		Commands: []*cli.Command{
			acpDoctorCmd(),
			acpHandshakeCmd(),
			acpPromptCmd(),
			acpNudgeCmd(),
			acpWatchCmd(),
			acpCancelCmd(),
			acpStatusCmd(),
			acpCaptureCmd(),
			acpSmokeCmd(),
		},
	}
}

func acpDoctorCmd() *cli.Command {
	return &cli.Command{
		Name:  "doctor",
		Usage: "Diagnose configured ACP runtimes and adapter commands",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "runtime", Usage: "Limit checks to one ACP runtime"},
			&cli.BoolFlag{Name: "handshake", Usage: "Spawn each runtime, verify ACP initialization, and send a minimal prompt"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			home, err := config.Home(cmd.Root().String("home"))
			if err != nil {
				return err
			}
			cfg, err := config.Load(home)
			if err != nil {
				return err
			}
			return runACPDoctor(ctx, home, cfg, cmd.String("runtime"), cmd.Bool("handshake"), os.Stdout)
		},
	}
}

func acpHandshakeCmd() *cli.Command {
	return &cli.Command{
		Name:  "handshake",
		Usage: "Start an ACP adapter, initialize it, and create a session",
		Flags: acpCommonFlags(30 * time.Second),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			p, err := newACPProbe(ctx, cmd)
			if err != nil {
				return err
			}
			defer p.runtimeInst.Kill(p.sessionName)
			st, err := p.runtimeInst.Status(p.sessionName)
			if err != nil {
				return err
			}
			fmt.Printf("acp handshake: runtime %q initialized session %q (pid %d)\n", p.runtimeName, st.RuntimeID, st.PID)
			return nil
		},
	}
}

func acpPromptCmd() *cli.Command {
	return &cli.Command{
		Name:      "prompt",
		Usage:     "Start an ACP session, send one prompt, and print captured updates",
		ArgsUsage: "<prompt>",
		Flags:     append(acpCommonFlags(60*time.Second), &cli.IntFlag{Name: "lines", Value: 80, Usage: "Captured lines to print"}),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			prompt := strings.Join(cmd.Args().Slice(), " ")
			if prompt == "" {
				return fmt.Errorf("usage: clankwork acp prompt <prompt>")
			}
			p, err := newACPProbe(ctx, cmd)
			if err != nil {
				return err
			}
			defer p.runtimeInst.Kill(p.sessionName)
			opCtx, cancel := p.context(ctx)
			defer cancel()
			stop, err := p.runtimeInst.PromptWithContext(opCtx, p.sessionName, prompt)
			if err != nil {
				return err
			}
			fmt.Printf("acp prompt: stop_reason=%q\n", stop)
			return p.printCapture(cmd.Int("lines"))
		},
	}
}

func acpNudgeCmd() *cli.Command {
	return &cli.Command{
		Name:  "nudge",
		Usage: "Send an initial prompt and then a follow-up message in the same ACP session",
		Flags: append(acpCommonFlags(90*time.Second),
			&cli.StringFlag{Name: "initial", Value: "Say ready.", Usage: "Initial prompt"},
			&cli.StringFlag{Name: "message", Usage: "Follow-up message to send"},
			&cli.IntFlag{Name: "lines", Value: 120, Usage: "Captured lines to print"},
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			msg := cmd.String("message")
			if msg == "" {
				return fmt.Errorf("--message is required")
			}
			p, err := newACPProbe(ctx, cmd)
			if err != nil {
				return err
			}
			defer p.runtimeInst.Kill(p.sessionName)
			opCtx, cancel := p.context(ctx)
			stop, err := p.runtimeInst.PromptWithContext(opCtx, p.sessionName, cmd.String("initial"))
			cancel()
			if err != nil {
				return err
			} else {
				fmt.Printf("acp nudge: initial stop_reason=%q\n", stop)
			}
			opCtx, cancel = p.context(ctx)
			stop, err = p.runtimeInst.PromptWithContext(opCtx, p.sessionName, msg)
			cancel()
			if err != nil {
				return err
			} else {
				fmt.Printf("acp nudge: follow_up stop_reason=%q\n", stop)
			}
			return p.printCapture(cmd.Int("lines"))
		},
	}
}

func acpWatchCmd() *cli.Command {
	return &cli.Command{
		Name:      "watch",
		Usage:     "Start an ACP session, send a prompt, and stream adapter events live",
		ArgsUsage: "<prompt>",
		Flags:     acpCommonFlags(60 * time.Second),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			prompt := strings.Join(cmd.Args().Slice(), " ")
			if prompt == "" {
				return fmt.Errorf("usage: clankwork acp watch <prompt>")
			}
			p, err := newACPProbe(ctx, cmd)
			if err != nil {
				return err
			}
			defer p.runtimeInst.Kill(p.sessionName)
			events, err := p.runtimeInst.Events(p.sessionName)
			if err != nil {
				return err
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				for line := range events {
					fmt.Println(line)
				}
			}()
			opCtx, cancel := p.context(ctx)
			defer cancel()
			stop, err := p.runtimeInst.PromptWithContext(opCtx, p.sessionName, prompt)
			if err != nil {
				return err
			}
			fmt.Printf("acp watch: stop_reason=%q\n", stop)
			return nil
		},
	}
}

func acpCancelCmd() *cli.Command {
	return &cli.Command{
		Name:      "cancel",
		Usage:     "Start a prompt, send session/cancel after a delay, and report the result",
		ArgsUsage: "<prompt>",
		Flags: append(acpCommonFlags(60*time.Second),
			&cli.DurationFlag{Name: "after", Value: 2 * time.Second, Usage: "Delay before sending session/cancel"},
			&cli.IntFlag{Name: "lines", Value: 120, Usage: "Captured lines to print"},
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			prompt := strings.Join(cmd.Args().Slice(), " ")
			if prompt == "" {
				return fmt.Errorf("usage: clankwork acp cancel <prompt>")
			}
			p, err := newACPProbe(ctx, cmd)
			if err != nil {
				return err
			}
			defer p.runtimeInst.Kill(p.sessionName)
			type result struct {
				stop string
				err  error
			}
			resultCh := make(chan result, 1)
			go func() {
				opCtx, cancel := p.context(ctx)
				defer cancel()
				stop, err := p.runtimeInst.PromptWithContext(opCtx, p.sessionName, prompt)
				resultCh <- result{stop: stop, err: err}
			}()
			time.Sleep(cmd.Duration("after"))
			if err := p.runtimeInst.Cancel(p.sessionName); err != nil {
				return err
			}
			res := <-resultCh
			if res.err != nil {
				fmt.Printf("acp cancel: prompt returned error: %v\n", res.err)
			} else {
				fmt.Printf("acp cancel: stop_reason=%q\n", res.stop)
			}
			return p.printCapture(cmd.Int("lines"))
		},
	}
}

func acpStatusCmd() *cli.Command {
	return &cli.Command{
		Name:  "status",
		Usage: "Start an ACP session and print runtime status",
		Flags: acpCommonFlags(30 * time.Second),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			p, err := newACPProbe(ctx, cmd)
			if err != nil {
				return err
			}
			defer p.runtimeInst.Kill(p.sessionName)
			st, err := p.runtimeInst.Status(p.sessionName)
			if err != nil {
				return err
			}
			return printJSON(st)
		},
	}
}

func acpCaptureCmd() *cli.Command {
	return &cli.Command{
		Name:  "capture",
		Usage: "Start an ACP session, optionally send a prompt, and print buffered protocol output",
		Flags: append(acpCommonFlags(60*time.Second),
			&cli.StringFlag{Name: "prompt", Usage: "Optional prompt to send before capture"},
			&cli.IntFlag{Name: "lines", Value: 120, Usage: "Captured lines to print"},
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			p, err := newACPProbe(ctx, cmd)
			if err != nil {
				return err
			}
			defer p.runtimeInst.Kill(p.sessionName)
			if prompt := cmd.String("prompt"); prompt != "" {
				opCtx, cancel := p.context(ctx)
				stop, err := p.runtimeInst.PromptWithContext(opCtx, p.sessionName, prompt)
				cancel()
				if err != nil {
					return err
				} else {
					fmt.Printf("acp capture: stop_reason=%q\n", stop)
				}
			}
			return p.printCapture(cmd.Int("lines"))
		},
	}
}

func acpSmokeCmd() *cli.Command {
	return &cli.Command{
		Name:  "smoke",
		Usage: "Compatibility alias for handshake, with optional prompt",
		Flags: append(acpCommonFlags(30*time.Second),
			&cli.StringFlag{Name: "prompt", Usage: "Optional prompt to send after session creation"},
			&cli.IntFlag{Name: "lines", Value: 40, Usage: "Captured lines to print"},
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			p, err := newACPProbe(ctx, cmd)
			if err != nil {
				return err
			}
			defer p.runtimeInst.Kill(p.sessionName)
			fmt.Printf("acp smoke: runtime %q initialized and created a session\n", p.runtimeName)
			if prompt := cmd.String("prompt"); prompt != "" {
				opCtx, cancel := p.context(ctx)
				defer cancel()
				stopReason, err := p.runtimeInst.PromptWithContext(opCtx, p.sessionName, prompt)
				if err != nil {
					return err
				}
				fmt.Printf("acp smoke: prompt stopped with reason %q\n", stopReason)
				return p.printCapture(cmd.Int("lines"))
			}
			return nil
		},
	}
}

func acpCommonFlags(timeout time.Duration) []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "runtime", Value: "pi-acp", Usage: "ACP runtime from config"},
		&cli.StringFlag{Name: "cwd", Usage: "Working directory for the ACP session"},
		&cli.DurationFlag{Name: "timeout", Value: timeout, Usage: "Operation timeout"},
	}
}

func newACPProbe(ctx context.Context, cmd *cli.Command) (*acpProbe, error) {
	home, err := config.Home(cmd.Root().String("home"))
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(home)
	if err != nil {
		return nil, err
	}
	runtimeName := cmd.String("runtime")
	rt, ok := cfg.Runtimes[runtimeName]
	if !ok {
		return nil, fmt.Errorf("unknown runtime %q", runtimeName)
	}
	if transport := config.RuntimeTransport(rt); transport != config.TransportACP {
		return nil, fmt.Errorf("runtime %q uses transport %q, not %q", runtimeName, transport, config.TransportACP)
	}
	cwd := cmd.String("cwd")
	if cwd == "" {
		cwd, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	if !filepath.IsAbs(cwd) {
		cwd, err = filepath.Abs(cwd)
		if err != nil {
			return nil, err
		}
	}

	p := &acpProbe{
		home:        home,
		runtimeName: runtimeName,
		runtime:     rt,
		cwd:         cwd,
		timeout:     cmd.Duration("timeout"),
		sessionName: "clankwork-acp-" + sanitizeRuntimeName(runtimeName) + "-" + fmt.Sprint(time.Now().UnixNano()),
		runtimeInst: worker.NewACPRuntime(filepath.Join(home, "logs")),
	}
	env, err := runtimeenv.Build(home, runtimeName, rt, map[string]string{
		"HOME": os.Getenv("HOME"),
		"PATH": os.Getenv("PATH"),
	})
	if err != nil {
		return nil, err
	}
	opCtx, cancel := p.context(ctx)
	defer cancel()
	if err := p.runtimeInst.SpawnWithContext(opCtx, p.sessionName, cwd, rt.Command, rt.Args, env); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *acpProbe) context(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, p.timeout)
}

func (p *acpProbe) printCapture(lines int) error {
	out, err := p.runtimeInst.CapturePane(p.sessionName, lines)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func sanitizeRuntimeName(name string) string {
	name = strings.ReplaceAll(name, "/", "-")
	name = strings.ReplaceAll(name, " ", "-")
	return name
}

type acpDoctorTarget struct {
	Name       string
	Runtime    config.RuntimeConfig
	CommandBin string
}

func runACPDoctor(ctx context.Context, home string, cfg *config.Config, runtimeFilter string, handshake bool, out io.Writer) error {
	targets, err := acpDoctorTargets(cfg, runtimeFilter)
	if err != nil {
		return err
	}

	fmt.Fprintln(out, "clankwork acp doctor")
	allOK := true
	for _, target := range targets {
		if err := diagnoseACPDoctorTarget(ctx, home, target, handshake, out); err != nil {
			allOK = false
			fmt.Fprintf(out, "  status: FAIL  %v\n", err)
		}
	}
	if !allOK {
		return fmt.Errorf("one or more ACP runtimes failed diagnostics")
	}
	return nil
}

func acpDoctorTargets(cfg *config.Config, runtimeFilter string) ([]acpDoctorTarget, error) {
	if runtimeFilter != "" {
		rt, ok := cfg.Runtimes[runtimeFilter]
		if !ok {
			return nil, fmt.Errorf("unknown runtime %q", runtimeFilter)
		}
		if transport := config.RuntimeTransport(rt); transport != config.TransportACP {
			return nil, fmt.Errorf("runtime %q uses transport %q, not %q", runtimeFilter, transport, config.TransportACP)
		}
		return []acpDoctorTarget{{Name: runtimeFilter, Runtime: rt}}, nil
	}

	names := make([]string, 0, len(cfg.Runtimes))
	for name, rt := range cfg.Runtimes {
		if config.RuntimeTransport(rt) == config.TransportACP {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	targets := make([]acpDoctorTarget, 0, len(names))
	for _, name := range names {
		targets = append(targets, acpDoctorTarget{Name: name, Runtime: cfg.Runtimes[name]})
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("no ACP runtimes configured")
	}
	return targets, nil
}

func diagnoseACPDoctorTarget(ctx context.Context, home string, target acpDoctorTarget, handshake bool, out io.Writer) error {
	rt := target.Runtime
	transport := config.RuntimeTransport(rt)
	fmt.Fprintf(out, "runtime %q\n", target.Name)
	fmt.Fprintf(out, "  transport: %s\n", transport)
	fmt.Fprintf(out, "  command: %s\n", rt.Command)
	if len(rt.Args) == 0 {
		fmt.Fprintln(out, "  args: []")
	} else {
		fmt.Fprintf(out, "  args: %q\n", rt.Args)
	}

	env, err := runtimeenv.Build(home, target.Name, rt, map[string]string{
		"HOME": os.Getenv("HOME"),
		"PATH": os.Getenv("PATH"),
	})
	if err != nil {
		return fmt.Errorf("runtime env: %w", err)
	}
	commandPath, err := worker.ResolveCommand(rt.Command, env)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "  command_path: %s\n", commandPath)

	if !handshake {
		describeACPDoctorTarget(out, home, target.Name, rt, env)
		fmt.Fprintln(out, "  handshake: skipped")
		return nil
	}

	rtRunner := worker.NewACPRuntime(filepath.Join(home, "logs"))
	sessionName := "clankwork-acp-doctor-" + sanitizeRuntimeName(target.Name) + "-" + fmt.Sprint(time.Now().UnixNano())
	workdir := home
	if cwd, err := os.Getwd(); err == nil {
		workdir = cwd
	}
	describeACPDoctorTarget(out, home, target.Name, rt, env)
	hCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := rtRunner.SpawnWithContext(hCtx, sessionName, workdir, commandPath, rt.Args, env); err != nil {
		return fmt.Errorf("handshake spawn: %w", err)
	}
	defer rtRunner.Kill(sessionName)
	st, err := rtRunner.Status(sessionName)
	if err != nil {
		return fmt.Errorf("handshake status: %w", err)
	}
	fmt.Fprintf(out, "  handshake: ok (session %q pid %d)\n", st.RuntimeID, st.PID)
	promptCtx, promptCancel := context.WithTimeout(ctx, 30*time.Second)
	defer promptCancel()
	stopReason, err := rtRunner.PromptWithContext(promptCtx, sessionName, "Reply with exactly: ready")
	if err != nil {
		return doctorProbeError(rtRunner, sessionName, fmt.Errorf("prompt probe: %w", err))
	}
	if stopReason == "error" {
		return doctorProbeError(rtRunner, sessionName, fmt.Errorf("prompt probe: stop reason %q", stopReason))
	}
	fmt.Fprintf(out, "  prompt: ok (stop_reason %q)\n", stopReason)
	return nil
}

func describeACPDoctorTarget(out io.Writer, home, runtimeName string, rt config.RuntimeConfig, env map[string]string) {
	summary := runtimeenv.Describe(home, runtimeName, rt, env)
	if summary.Adapter == "pi" {
		if summary.Provider != "" {
			fmt.Fprintf(out, "  pi_provider: %s (%s)\n", summary.Provider, summary.ProviderSource)
		}
		if summary.AuthMode != "" {
			if summary.AuthSource != "" {
				fmt.Fprintf(out, "  pi_auth: %s (%s)\n", summary.AuthMode, summary.AuthSource)
			} else {
				fmt.Fprintf(out, "  pi_auth: %s\n", summary.AuthMode)
			}
		}
		fmt.Fprintf(out, "  pi_agent_dir: %s\n", summary.AgentDir)
	}
}

func doctorProbeError(rt *worker.ACPRuntime, sessionName string, err error) error {
	capture, capErr := rt.CapturePane(sessionName, 20)
	if capErr != nil || strings.TrimSpace(capture) == "" {
		return err
	}
	hint := ""
	if strings.Contains(capture, "Connection error.") {
		hint = "\nhint: provider/model resolution succeeded, but the backend request failed after turn start; check network access and provider reachability from the daemon environment."
	}
	return fmt.Errorf("%w\nadapter log tail:\n%s%s", err, strings.TrimSpace(capture), hint)
}
