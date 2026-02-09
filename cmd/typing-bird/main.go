package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	defaultTimeout     = 30 * time.Second
	defaultDelay       = 15 * time.Millisecond
	defaultIdleSamples = 5
	interruptWindow    = 5 * time.Second
	enterKey           = "Enter"
)

var verboseLogging bool

func main() {
	os.Exit(run())
}

func run() int {
	timeoutValue := defaultTimeout.String()
	delayValue := defaultDelay.String()
	inject := false
	targetPaneValue := ""
	verbose := false

	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s -t|--timeout <duration> <tmux-session-name> [messages-list ...]\n", os.Args[0])
		fmt.Fprintln(flag.CommandLine.Output(), "")
		fmt.Fprintln(flag.CommandLine.Output(), "Periodically sends the next message to a tmux session after terminal-idle timeout,")
		fmt.Fprintln(flag.CommandLine.Output(), "appending a newline/Enter and cycling back to the first message.")
		fmt.Fprintln(flag.CommandLine.Output(), "")
		fmt.Fprintln(flag.CommandLine.Output(), "Flags:")
		fmt.Fprintf(flag.CommandLine.Output(), "  -t, --timeout         terminal-idle timeout window before next send (default: %s)\n", defaultTimeout)
		fmt.Fprintf(flag.CommandLine.Output(), "  -d, --delay           key input delay duration (default: %s)\n", defaultDelay)
		fmt.Fprintln(flag.CommandLine.Output(), "  -v, --verbose         enable debug logging")
		fmt.Fprintln(flag.CommandLine.Output(), "  -i, --inject          inject into target session as bottom 5-line pane")
		fmt.Fprintln(flag.CommandLine.Output(), "")
		fmt.Fprintln(flag.CommandLine.Output(), "Examples:")
		fmt.Fprintf(flag.CommandLine.Output(), "  %s -t 30m foobar message1 message2 message3\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "  %s --timeout 45s foobar\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "  %s -t 1m -d 25ms foobar \"line1\\nline2\"\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "  %s -i foobar message1 message2\n", os.Args[0])
	}

	flag.StringVar(&timeoutValue, "t", timeoutValue, "terminal-idle timeout window before next send (e.g. 30s, 15m, 1h)")
	flag.StringVar(&timeoutValue, "timeout", timeoutValue, "terminal-idle timeout window before next send (e.g. 30s, 15m, 1h)")
	flag.StringVar(&delayValue, "d", delayValue, "key input delay duration")
	flag.StringVar(&delayValue, "delay", delayValue, "key input delay duration")
	flag.BoolVar(&verbose, "v", false, "enable debug logging")
	flag.BoolVar(&verbose, "verbose", false, "enable debug logging")
	flag.BoolVar(&inject, "i", false, "inject as a detached bottom pane in the target session")
	flag.BoolVar(&inject, "inject", false, "inject as a detached bottom pane in the target session")
	// Internal flag used by injected child process to target the original pane.
	flag.StringVar(&targetPaneValue, "target-pane", "", "internal pane target for send-keys")
	flag.Parse()
	verboseLogging = verbose

	timeout, err := parseDuration(timeoutValue, "timeout", true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		return 2
	}

	delay, err := parseDuration(delayValue, "delay", false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		return 2
	}
	if inject && strings.TrimSpace(targetPaneValue) != "" {
		fmt.Fprintln(os.Stderr, "ERROR: inject mode cannot be combined with --target-pane")
		return 2
	}

	args := flag.Args()
	if len(args) < 1 {
		flag.Usage()
		return 2
	}

	session := args[0]
	messages := args[1:]
	if len(messages) == 0 {
		messages = []string{""}
	}

	if _, err := exec.LookPath("tmux"); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: tmux not found in PATH: %v\n", err)
		return 1
	}
	if err := tmuxSessionExists(session); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: tmux session %q not available: %v\n", session, err)
		return 1
	}
	if inject {
		exePath, err := os.Executable()
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: failed locating executable path: %v\n", err)
			return 1
		}
		exeBase := filepath.Base(exePath)
		currentPane := strings.TrimSpace(os.Getenv("TMUX_PANE"))
		skippedCurrentPane, err := tmuxRestartExistingBirdPanes(session, currentPane, exeBase)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: failed restarting existing typing-bird panes in session %q: %v\n", session, err)
			return 1
		}

		sendTargetPane, err := resolveInjectionSendTarget(session)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: failed resolving injection target pane for session %q: %v\n", session, err)
			return 1
		}

		childArgs := buildChildArgs(timeout, delay, session, messages, sendTargetPane, verbose)
		childCommand := shellCommandForExec(exePath, childArgs)
		injectedPaneID, err := tmuxInjectBottomPane(sendTargetPane, childCommand)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: failed injecting pane into session %q: %v\n", session, err)
			return 1
		}
		if err := tmuxMarkInjectedPane(injectedPaneID, sendTargetPane); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: failed marking injected pane %q: %v\n", injectedPaneID, err)
			return 1
		}
		if skippedCurrentPane && currentPane != "" {
			_ = tmuxKillPane(currentPane)
		}
		logf(
			"injected pane=%q target-pane=%q session=%q timeout=%s delay=%s messages=%d",
			injectedPaneID, sendTargetPane, session, timeout, delay, len(messages),
		)
		return 0
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	launchCommand := buildLaunchCommand(os.Args)
	interruptCode := atomic.Int32{}
	stopInterrupts := installInterruptHandlers(cancel, launchCommand, interruptWindow, &interruptCode)
	defer stopInterrupts()

	sendTarget := strings.TrimSpace(targetPaneValue)
	if sendTarget == "" {
		resolved, err := tmuxPreferredSendPaneForSession(session)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: failed resolving target pane for session %q: %v\n", session, err)
			return 1
		}
		sendTarget = resolved
	}

	logf(
		"session=%q send-target=%q idle-timeout=%s delay=%s messages=%d",
		session, sendTarget, timeout, delay, len(messages),
	)
	if len(args) == 1 {
		logf("no messages supplied; sending newline only each timeout")
	}

	messageIndex := 0

	for {
		baseLen, err := waitForTargetIdle(ctx, sendTarget, defaultIdleSamples, timeout)
		if err != nil {
			if err == context.Canceled {
				code := interruptCode.Load()
				if code != 0 {
					return int(code)
				}
				logf("shutdown signal received, exiting")
				return 0
			}
			fmt.Fprintf(os.Stderr, "ERROR: idle wait failed for target %q in session %q: %v\n", sendTarget, session, err)
			return 1
		}
		logf("idle detected on pane-id=%q: sample1=%d bytes", sendTarget, baseLen)

		message := messages[messageIndex]
		if err := tmuxSendMessage(sendTarget, message, delay); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: failed sending message #%d to target %q in session %q: %v\n", messageIndex+1, sendTarget, session, err)
			return 1
		}

		logf("sent message %d/%d: %q", messageIndex+1, len(messages), message)
		messageIndex = (messageIndex + 1) % len(messages)
	}
}

func parseDuration(raw, name string, requirePositive bool) (time.Duration, error) {
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", name, raw, err)
	}
	if requirePositive {
		if value <= 0 {
			return 0, fmt.Errorf("%s must be greater than 0 (got %s)", name, value)
		}
		return value, nil
	}
	if value < 0 {
		return 0, fmt.Errorf("%s must be >= 0 (got %s)", name, value)
	}
	return value, nil
}

func tmuxSessionExists(session string) error {
	cmd := exec.Command("tmux", "has-session", "-t", session)
	return cmd.Run()
}

func tmuxCaptureTarget(target string) ([]byte, error) {
	cmd := exec.Command("tmux", "capture-pane", "-p", "-t", target)
	return cmd.Output()
}

func waitForTargetIdle(ctx context.Context, target string, samples int, duration time.Duration) (int, error) {
	for {
		select {
		case <-ctx.Done():
			return 0, context.Canceled
		default:
		}

		allEqual, baseLen, diffsBase, diffsPrev, err := idleSamplesTarget(ctx, target, samples, duration)
		if err != nil {
			if err == context.Canceled {
				return 0, context.Canceled
			}
			if ok, _ := tmuxTargetExists(target); !ok {
				return 0, fmt.Errorf("tmux target %q no longer exists", target)
			}
			if sleepErr := sleepWithContext(ctx, 200*time.Millisecond); sleepErr != nil {
				return 0, sleepErr
			}
			continue
		}
		if allEqual {
			return baseLen, nil
		}
		debugf("not idle yet on %q; %s", target, formatIdleDifferences(diffsBase, diffsPrev))
	}
}

// idleSamplesTarget mirrors idle-latch sampling: capture N times across total duration.
func idleSamplesTarget(ctx context.Context, target string, samples int, duration time.Duration) (bool, int, []int, []int, error) {
	if samples < 1 {
		return false, 0, nil, nil, fmt.Errorf("samples must be >= 1")
	}
	var interval time.Duration
	if samples > 1 {
		interval = time.Duration(int64(duration) / int64(samples-1))
	}

	caps := make([][]byte, 0, samples)
	for i := 0; i < samples; i++ {
		select {
		case <-ctx.Done():
			return false, 0, nil, nil, context.Canceled
		default:
		}

		b, err := tmuxCaptureTarget(target)
		if err != nil {
			return false, 0, nil, nil, err
		}
		caps = append(caps, b)
		if i < samples-1 && interval > 0 {
			if err := sleepWithContext(ctx, interval); err != nil {
				return false, 0, nil, nil, err
			}
		}
	}

	base := caps[0]
	allEqual := true
	diffsFromBase := make([]int, samples)
	diffsFromPrev := make([]int, samples)
	for i := 1; i < samples; i++ {
		if !bytes.Equal(base, caps[i]) {
			allEqual = false
		}
		diffsFromBase[i] = byteDiffCount(base, caps[i])
		diffsFromPrev[i] = byteDiffCount(caps[i-1], caps[i])
	}
	return allEqual, len(base), diffsFromBase, diffsFromPrev, nil
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return context.Canceled
	case <-timer.C:
		return nil
	}
}

func tmuxTargetExists(target string) (bool, error) {
	cmd := exec.Command("tmux", "display-message", "-p", "-t", target, "#{pane_id}")
	if err := cmd.Run(); err != nil {
		return false, err
	}
	return true, nil
}

func byteDiffCount(a, b []byte) int {
	min := len(a)
	if len(b) < min {
		min = len(b)
	}
	diffs := 0
	for i := 0; i < min; i++ {
		if a[i] != b[i] {
			diffs++
		}
	}
	diffs += abs(len(a) - len(b))
	return diffs
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func formatIdleDifferences(diffsBase, diffsPrev []int) string {
	var b strings.Builder
	b.WriteString("differences relative to sample 1: ")
	first := true
	for i := 1; i < len(diffsBase); i++ {
		if diffsBase[i] != 0 {
			if !first {
				b.WriteString(", ")
			}
			first = false
			fmt.Fprintf(&b, "sample %d: base=%d prev=%d", i+1, diffsBase[i], diffsPrev[i])
		}
	}
	return b.String()
}

func tmuxRestartExistingBirdPanes(session, currentPane, commandName string) (bool, error) {
	out, err := exec.Command("tmux", "list-panes", "-t", session, "-F", "#{pane_id}\t#{@typing_bird_injected}\t#{pane_current_command}").Output()
	if err != nil {
		return false, err
	}
	panes := parseBirdPaneIDs(string(out), commandName)
	skippedCurrent := false
	for _, paneID := range panes {
		if paneID == currentPane && currentPane != "" {
			skippedCurrent = true
			continue
		}
		_ = tmuxSendKey(paneID, "C-c", 0)
		time.Sleep(150 * time.Millisecond)
		_ = tmuxKillPane(paneID)
	}
	return skippedCurrent, nil
}

func parseBirdPaneIDs(raw, commandName string) []string {
	lines := strings.Split(raw, "\n")
	seen := make(map[string]struct{})
	panes := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		paneID := strings.TrimSpace(parts[0])
		injectedFlag := strings.TrimSpace(parts[1])
		currentCommand := strings.TrimSpace(parts[2])
		if paneID == "" {
			continue
		}
		if injectedFlag != "1" && currentCommand != commandName && currentCommand != "typing-bird" {
			continue
		}
		if _, exists := seen[paneID]; exists {
			continue
		}
		seen[paneID] = struct{}{}
		panes = append(panes, paneID)
	}
	return panes
}

func resolveInjectionSendTarget(session string) (string, error) {
	if pane := strings.TrimSpace(os.Getenv("TMUX_PANE")); pane != "" {
		belongs, err := tmuxPaneBelongsToSession(pane, session)
		if err == nil && belongs {
			injected, injErr := tmuxPaneIsInjected(pane)
			if injErr == nil && !injected {
				return pane, nil
			}
		}
	}
	return tmuxPreferredSendPaneForSession(session)
}

func tmuxPaneIsInjected(paneID string) (bool, error) {
	out, err := exec.Command("tmux", "display-message", "-p", "-t", paneID, "#{@typing_bird_injected}").Output()
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == "1", nil
}

func tmuxPreferredSendPaneForSession(session string) (string, error) {
	out, err := exec.Command("tmux", "list-panes", "-t", session, "-F", "#{pane_id}\t#{pane_active}\t#{@typing_bird_injected}").Output()
	if err != nil {
		return "", err
	}
	pane := pickPreferredSendPane(string(out))
	if pane != "" {
		return pane, nil
	}
	return "", fmt.Errorf("no non-injected pane found in session")
}

func pickPreferredSendPane(raw string) string {
	lines := strings.Split(raw, "\n")
	firstNonInjected := ""
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		paneID := strings.TrimSpace(parts[0])
		active := strings.TrimSpace(parts[1])
		injected := strings.TrimSpace(parts[2]) == "1"
		if paneID == "" || injected {
			continue
		}
		if active == "1" {
			return paneID
		}
		if firstNonInjected == "" {
			firstNonInjected = paneID
		}
	}
	return firstNonInjected
}

func tmuxPaneBelongsToSession(paneID, session string) (bool, error) {
	out, err := exec.Command("tmux", "display-message", "-p", "-t", paneID, "#{session_name}").Output()
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == session, nil
}

func tmuxActivePaneForSession(session string) (string, error) {
	out, err := exec.Command("tmux", "display-message", "-p", "-t", session, "#{pane_id}").Output()
	if err != nil {
		return "", err
	}
	pane := strings.TrimSpace(string(out))
	if pane == "" {
		return "", fmt.Errorf("tmux returned empty pane id")
	}
	return pane, nil
}

func buildChildArgs(timeout, delay time.Duration, session string, messages []string, targetPane string, verbose bool) []string {
	args := []string{"-t", timeout.String(), "-d", delay.String()}
	if verbose {
		args = append(args, "--verbose")
	}
	if strings.TrimSpace(targetPane) != "" {
		args = append(args, "--target-pane", targetPane)
	}
	args = append(args, session)
	args = append(args, messages...)
	return args
}

func buildLaunchCommand(args []string) string {
	if len(args) == 0 {
		return ""
	}
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		parts = append(parts, shellQuoteSingle(arg))
	}
	return strings.Join(parts, " ")
}

func installInterruptHandlers(cancel context.CancelFunc, launchCommand string, window time.Duration, exitCode *atomic.Int32) (stop func()) {
	c := make(chan os.Signal, 2)
	done := make(chan struct{})
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	var last time.Time
	go func() {
		for {
			select {
			case <-done:
				return
			case sig, ok := <-c:
				if !ok {
					return
				}
				now := time.Now()
				if sig == syscall.SIGTERM {
					fmt.Fprintf(os.Stderr, "[%s] INFO: SIGTERM received; exiting.\n", now.Format(time.RFC3339))
					exitCode.Store(143)
					cancel()
					return
				}
				if !last.IsZero() && now.Sub(last) <= window {
					fmt.Fprintf(os.Stderr, "[%s] INFO: Second Ctrl-C within %s; exiting.\n", now.Format(time.RFC3339), window)
					exitCode.Store(130)
					cancel()
					return
				}
				fmt.Fprintf(os.Stderr, "[%s] INFO: Ctrl-C received; restart with:\n", now.Format(time.RFC3339))
				if launchCommand != "" {
					fmt.Fprintf(os.Stderr, "$ %s\n", launchCommand)
				} else {
					fmt.Fprintln(os.Stderr, "$ <unknown command>")
				}
				fmt.Fprintf(os.Stderr, "[%s] INFO: Press Ctrl-C again within %s to exit.\n", time.Now().Format(time.RFC3339), window)
				last = now
			}
		}
	}()
	return func() {
		signal.Stop(c)
		close(done)
	}
}

func shellCommandForExec(executable string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, shellQuoteSingle(executable))
	for _, arg := range args {
		parts = append(parts, shellQuoteSingle(arg))
	}
	return strings.Join(parts, " ")
}

func tmuxSplitBottomPaneArgs(targetPane, shellCommand string) []string {
	return []string{
		"split-window",
		"-v",
		"-d",
		"-l",
		"5",
		"-P",
		"-F",
		"#{pane_id}",
		"-t",
		targetPane,
		shellCommand,
	}
}

func tmuxInjectBottomPane(targetPane, shellCommand string) (string, error) {
	cmdArgs := tmuxSplitBottomPaneArgs(targetPane, shellCommand)
	out, err := exec.Command("tmux", cmdArgs...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	paneID := strings.TrimSpace(string(out))
	if paneID == "" {
		return "", fmt.Errorf("tmux split-window returned empty pane id")
	}
	return paneID, nil
}

func tmuxMarkInjectedPane(paneID, sendTargetPane string) error {
	if err := exec.Command("tmux", "set-option", "-p", "-t", paneID, "@typing_bird_injected", "1").Run(); err != nil {
		return err
	}
	if err := exec.Command("tmux", "set-option", "-p", "-t", paneID, "@typing_bird_send_target", sendTargetPane).Run(); err != nil {
		return err
	}
	return nil
}

func tmuxKillPane(paneID string) error {
	return exec.Command("tmux", "kill-pane", "-t", paneID).Run()
}

func tmuxSendMessage(target, message string, keyDelay time.Duration) error {
	for _, action := range messageSendActions(message, enterKey) {
		if action.literal {
			if err := tmuxSendLiteral(target, action.value); err != nil {
				return err
			}
			continue
		}
		if err := tmuxSendKey(target, action.value, keyDelay); err != nil {
			return err
		}
	}
	return nil
}

func tmuxSendLiteral(session, value string) error {
	cmd := exec.Command("tmux", "send-keys", "-t", session, "-l", "--", value)
	return cmd.Run()
}

func tmuxSendKey(session, key string, delay time.Duration) error {
	if delay > 0 {
		time.Sleep(delay)
	}
	cmd := exec.Command(
		"bash",
		"-c",
		fmt.Sprintf("tmux send-keys -t %s %s", shellQuoteSingle(session), shellQuoteSingle(key)),
	)
	return cmd.Run()
}

type sendAction struct {
	value   string
	literal bool
}

func messageSendActions(message, enter string) []sendAction {
	actions := make([]sendAction, 0, 2)
	var current strings.Builder
	prevWasCR := false

	flushLiteral := func() {
		if current.Len() == 0 {
			return
		}
		actions = append(actions, sendAction{value: current.String(), literal: true})
		current.Reset()
	}

	for _, r := range message {
		switch r {
		case '\r':
			flushLiteral()
			actions = append(actions, sendAction{value: enter})
			prevWasCR = true
		case '\n':
			if prevWasCR {
				prevWasCR = false
				continue
			}
			flushLiteral()
			actions = append(actions, sendAction{value: enter})
			prevWasCR = false
		default:
			prevWasCR = false
			current.WriteRune(r)
		}
	}

	flushLiteral()
	actions = append(actions, sendAction{value: enter})
	return actions
}

func shellQuoteSingle(value string) string {
	if value == "" {
		return "''"
	}

	var builder strings.Builder
	builder.WriteByte('\'')
	for _, r := range value {
		if r == '\'' {
			builder.WriteString("'\\''")
			continue
		}
		builder.WriteRune(r)
	}
	builder.WriteByte('\'')
	return builder.String()
}

func logf(format string, args ...any) {
	all := make([]any, 0, len(args)+1)
	all = append(all, time.Now().Format(time.RFC3339))
	all = append(all, args...)
	fmt.Fprintf(os.Stderr, "[%s] INFO: "+format+"\n", all...)
}

func debugf(format string, args ...any) {
	if !verboseLogging {
		return
	}
	all := make([]any, 0, len(args)+1)
	all = append(all, time.Now().Format(time.RFC3339))
	all = append(all, args...)
	fmt.Fprintf(os.Stderr, "[%s] DEBUG: "+format+"\n", all...)
}
