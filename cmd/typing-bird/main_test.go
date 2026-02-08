package main

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMessageSendActions(t *testing.T) {
	tests := []struct {
		name     string
		message  string
		enterKey string
		want     []sendAction
	}{
		{
			name:     "plain message gets one trailing enter",
			message:  "hello",
			enterKey: "Enter",
			want: []sendAction{
				{value: "hello", literal: true},
				{value: "Enter"},
			},
		},
		{
			name:     "empty message still sends enter",
			message:  "",
			enterKey: "Enter",
			want: []sendAction{
				{value: "Enter"},
			},
		},
		{
			name:     "lf becomes enter",
			message:  "one\ntwo",
			enterKey: "Enter",
			want: []sendAction{
				{value: "one", literal: true},
				{value: "Enter"},
				{value: "two", literal: true},
				{value: "Enter"},
			},
		},
		{
			name:     "cr becomes enter",
			message:  "one\rtwo",
			enterKey: "Enter",
			want: []sendAction{
				{value: "one", literal: true},
				{value: "Enter"},
				{value: "two", literal: true},
				{value: "Enter"},
			},
		},
		{
			name:     "crlf becomes one enter",
			message:  "one\r\ntwo",
			enterKey: "Enter",
			want: []sendAction{
				{value: "one", literal: true},
				{value: "Enter"},
				{value: "two", literal: true},
				{value: "Enter"},
			},
		},
		{
			name:     "consecutive delimiters send consecutive enters",
			message:  "a\n\nb",
			enterKey: "Enter",
			want: []sendAction{
				{value: "a", literal: true},
				{value: "Enter"},
				{value: "Enter"},
				{value: "b", literal: true},
				{value: "Enter"},
			},
		},
		{
			name:     "custom enter key",
			message:  "a\nb",
			enterKey: "C-m",
			want: []sendAction{
				{value: "a", literal: true},
				{value: "C-m"},
				{value: "b", literal: true},
				{value: "C-m"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := messageSendActions(tt.message, tt.enterKey)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("messageSendActions(%q, %q) = %#v; want %#v", tt.message, tt.enterKey, got, tt.want)
			}
		})
	}
}

func TestNextScheduledSendAccountsForSendDelay(t *testing.T) {
	base := time.Unix(0, 0).UTC()
	interval := 30 * time.Second
	previous := base.Add(interval)
	sentAt := base.Add(95 * time.Second)

	got := nextScheduledSend(previous, interval, sentAt)
	want := base.Add(120 * time.Second)
	if !got.Equal(want) {
		t.Fatalf("nextScheduledSend(...) = %s; want %s", got, want)
	}
}

func TestBuildChildArgsOmitsInjectFlagAndIncludesTargetPane(t *testing.T) {
	got := buildChildArgs(30*time.Second, 15*time.Millisecond, "foobar", []string{"m1", "m2"}, "%123")
	want := []string{"-t", "30s", "-d", "15ms", "--target-pane", "%123", "foobar", "m1", "m2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildChildArgs(...) = %#v; want %#v", got, want)
	}
	for _, arg := range got {
		if arg == "-i" || arg == "--inject" || strings.HasPrefix(arg, "--inject=") {
			t.Fatalf("buildChildArgs included inject flag unexpectedly: %#v", got)
		}
	}
}

func TestShellCommandForExecQuotesArguments(t *testing.T) {
	got := shellCommandForExec("/tmp/typing-bird", []string{"-t", "30s", "foo bar", "a'b"})
	want := "'/tmp/typing-bird' '-t' '30s' 'foo bar' 'a'\\''b'"
	if got != want {
		t.Fatalf("shellCommandForExec(...) = %q; want %q", got, want)
	}
}

func TestTmuxSplitBottomPaneArgsLayoutAndHeight(t *testing.T) {
	got := tmuxSplitBottomPaneArgs("%3", "'/bin/typing-bird' '-t' '30s' 'foo'")
	want := []string{
		"split-window",
		"-v",
		"-d",
		"-l",
		"5",
		"-P",
		"-F",
		"#{pane_id}",
		"-t",
		"%3",
		"'/bin/typing-bird' '-t' '30s' 'foo'",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tmuxSplitBottomPaneArgs(...) = %#v; want %#v", got, want)
	}
}

func TestParseBirdPaneIDs(t *testing.T) {
	raw := strings.Join([]string{
		"%1\t1\tbash",
		"%2\t\ttyping-bird",
		"%3\t\tvim",
		"%4\t\t" + "typing-bird",
		"",
	}, "\n")
	got := parseBirdPaneIDs(raw, "typing-bird")
	want := []string{"%1", "%2", "%4"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseBirdPaneIDs(...) = %#v; want %#v", got, want)
	}
}

func TestPickPreferredSendPane(t *testing.T) {
	raw := strings.Join([]string{
		"%9\t0\t1",
		"%2\t1\t",
		"%3\t0\t",
		"",
	}, "\n")
	got := pickPreferredSendPane(raw)
	if got != "%2" {
		t.Fatalf("pickPreferredSendPane(...) = %q; want %q", got, "%2")
	}
}

func TestPickPreferredSendPaneFallsBackToFirstNonInjected(t *testing.T) {
	raw := strings.Join([]string{
		"%9\t1\t1",
		"%3\t0\t",
		"%2\t0\t",
		"",
	}, "\n")
	got := pickPreferredSendPane(raw)
	if got != "%3" {
		t.Fatalf("pickPreferredSendPane(...) = %q; want %q", got, "%3")
	}
}
