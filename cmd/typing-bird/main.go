package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const (
	defaultTimeout = 30 * time.Second
	defaultDelay   = 15 * time.Millisecond
	enterKey       = "Enter"
)

func main() {
	os.Exit(run())
}

func run() int {
	timeoutValue := defaultTimeout.String()
	delayValue := defaultDelay.String()
	inject := false
	targetPaneValue := ""

	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s -t|--timeout <duration> <tmux-session-name> [messages-list ...]\n", os.Args[0])
		fmt.Fprintln(flag.CommandLine.Output(), "")
		fmt.Fprintln(flag.CommandLine.Output(), "Periodically sends the next message to a tmux session after idle timeout,")
		fmt.Fprintln(flag.CommandLine.Output(), "appending a newline/Enter and cycling back to the first message.")
		fmt.Fprintln(flag.CommandLine.Output(), "")
		fmt.Fprintln(flag.CommandLine.Output(), "Flags:")
		fmt.Fprintf(flag.CommandLine.Output(), "  -t, --timeout         idle timeout between sends (default: %s)\n", defaultTimeout)
		fmt.Fprintf(flag.CommandLine.Output(), "  -d, --delay           key input delay duration (default: %s)\n", defaultDelay)
		fmt.Fprintln(flag.CommandLine.Output(), "  -i, --inject          inject into target session as bottom 5-line pane")
		fmt.Fprintln(flag.CommandLine.Output(), "")
		fmt.Fprintln(flag.CommandLine.Output(), "Examples:")
		fmt.Fprintf(flag.CommandLine.Output(), "  %s -t 30m foobar message1 message2 message3\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "  %s --timeout 45s foobar\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "  %s -t 1m -d 25ms foobar \"line1\\nline2\"\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "  %s -i foobar message1 message2\n", os.Args[0])
	}

	flag.StringVar(&timeoutValue, "t", timeoutValue, "idle timeout between sends (e.g. 30s, 15m, 1h)")
	flag.StringVar(&timeoutValue, "timeout", timeoutValue, "idle timeout between sends (e.g. 30s, 15m, 1h)")
	flag.StringVar(&delayValue, "d", delayValue, "key input delay duration")
	flag.StringVar(&delayValue, "delay", delayValue, "key input delay duration")
	flag.BoolVar(&inject, "i", false, "inject as a detached bottom pane in the target session")
	flag.BoolVar(&inject, "inject", false, "inject as a detached bottom pane in the target session")
	// Internal flag used by injected child process to target the original pane.
	flag.StringVar(&targetPaneValue, "target-pane", "", "internal pane target for send-keys")
	flag.Parse()

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
		sendTargetPane, err := resolveInjectionSendTarget(session)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: failed resolving injection target pane for session %q: %v\n", session, err)
			return 1
		}
		exePath, err := os.Executable()
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: failed locating executable path: %v\n", err)
			return 1
		}

		childArgs := buildChildArgs(timeout, delay, session, messages, sendTargetPane)
		childCommand := shellCommandForExec(exePath, childArgs)
		injectedPaneID, err := tmuxInjectBottomPane(sendTargetPane, childCommand)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: failed injecting pane into session %q: %v\n", session, err)
			return 1
		}
		logf(
			"injected pane=%q target-pane=%q session=%q timeout=%s delay=%s messages=%d",
			injectedPaneID, sendTargetPane, session, timeout, delay, len(messages),
		)
		return 0
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	sendTarget := session
	if trimmed := strings.TrimSpace(targetPaneValue); trimmed != "" {
		sendTarget = trimmed
	}

	logf(
		"session=%q send-target=%q timeout=%s delay=%s messages=%d",
		session, sendTarget, timeout, delay, len(messages),
	)
	if len(args) == 1 {
		logf("no messages supplied; sending newline only each timeout")
	}

	nextSend := time.Now().Add(timeout)
	messageIndex := 0

	for {
		if err := waitUntil(ctx, nextSend); err != nil {
			if errors.Is(err, context.Canceled) {
				logf("shutdown signal received, exiting")
				return 0
			}
			fmt.Fprintf(os.Stderr, "ERROR: wait failed: %v\n", err)
			return 1
		}

		message := messages[messageIndex]
		if err := tmuxSendMessage(sendTarget, message, delay); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: failed sending message #%d to target %q in session %q: %v\n", messageIndex+1, sendTarget, session, err)
			return 1
		}

		sentAt := time.Now()
		logf("sent message %d/%d", messageIndex+1, len(messages))
		messageIndex = (messageIndex + 1) % len(messages)
		nextSend = nextScheduledSend(nextSend, timeout, sentAt)
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

func waitUntil(ctx context.Context, target time.Time) error {
	wait := time.Until(target)
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return context.Canceled
	case <-timer.C:
		return nil
	}
}

func nextScheduledSend(previous time.Time, interval time.Duration, sentAt time.Time) time.Time {
	next := previous.Add(interval)
	for !next.After(sentAt) {
		next = next.Add(interval)
	}
	return next
}

func tmuxSessionExists(session string) error {
	cmd := exec.Command("tmux", "has-session", "-t", session)
	return cmd.Run()
}

func resolveInjectionSendTarget(session string) (string, error) {
	if pane := strings.TrimSpace(os.Getenv("TMUX_PANE")); pane != "" {
		belongs, err := tmuxPaneBelongsToSession(pane, session)
		if err == nil && belongs {
			return pane, nil
		}
	}
	return tmuxActivePaneForSession(session)
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

func buildChildArgs(timeout, delay time.Duration, session string, messages []string, targetPane string) []string {
	args := []string{"-t", timeout.String(), "-d", delay.String()}
	if strings.TrimSpace(targetPane) != "" {
		args = append(args, "--target-pane", targetPane)
	}
	args = append(args, session)
	args = append(args, messages...)
	return args
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
