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

	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s -t|--timeout <duration> <tmux-session-name> [messages-list ...]\n", os.Args[0])
		fmt.Fprintln(flag.CommandLine.Output(), "")
		fmt.Fprintln(flag.CommandLine.Output(), "Periodically sends the next message to a tmux session after idle timeout,")
		fmt.Fprintln(flag.CommandLine.Output(), "appending a newline/Enter and cycling back to the first message.")
		fmt.Fprintln(flag.CommandLine.Output(), "")
		fmt.Fprintln(flag.CommandLine.Output(), "Flags:")
		fmt.Fprintf(flag.CommandLine.Output(), "  -t, --timeout         idle timeout between sends (default: %s)\n", defaultTimeout)
		fmt.Fprintf(flag.CommandLine.Output(), "  -d, --delay           key input delay duration (default: %s)\n", defaultDelay)
		fmt.Fprintln(flag.CommandLine.Output(), "")
		fmt.Fprintln(flag.CommandLine.Output(), "Examples:")
		fmt.Fprintf(flag.CommandLine.Output(), "  %s -t 30m foobar message1 message2 message3\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "  %s --timeout 45s foobar\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "  %s -t 1m -d 25ms foobar \"line1\\nline2\"\n", os.Args[0])
	}

	flag.StringVar(&timeoutValue, "t", timeoutValue, "idle timeout between sends (e.g. 30s, 15m, 1h)")
	flag.StringVar(&timeoutValue, "timeout", timeoutValue, "idle timeout between sends (e.g. 30s, 15m, 1h)")
	flag.StringVar(&delayValue, "d", delayValue, "key input delay duration")
	flag.StringVar(&delayValue, "delay", delayValue, "key input delay duration")
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logf(
		"session=%q timeout=%s delay=%s messages=%d",
		session, timeout, delay, len(messages),
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
		if err := tmuxSendMessage(session, message, delay); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: failed sending message #%d to session %q: %v\n", messageIndex+1, session, err)
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

func tmuxSendMessage(session, message string, keyDelay time.Duration) error {
	for _, action := range messageSendActions(message, enterKey) {
		if action.literal {
			if err := tmuxSendLiteral(session, action.value); err != nil {
				return err
			}
			continue
		}
		if err := tmuxSendKey(session, action.value, keyDelay); err != nil {
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
