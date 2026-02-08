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

func TestByteDiffCount(t *testing.T) {
	a := []byte("abcdef")
	b := []byte("abcXefghi")
	got := byteDiffCount(a, b)
	if got != 4 {
		t.Fatalf("byteDiffCount(...) = %d; want %d", got, 4)
	}
}

func TestBuildChildArgsOmitsInjectFlagAndIncludesTargetPane(t *testing.T) {
	got := buildChildArgs(30*time.Second, 15*time.Millisecond, "foobar", []string{"m1", "m2"}, "%123", false)
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

func TestBuildChildArgsIncludesVerboseWhenEnabled(t *testing.T) {
	got := buildChildArgs(30*time.Second, 15*time.Millisecond, "foobar", []string{"m1"}, "%123", true)
	want := []string{"-t", "30s", "-d", "15ms", "--verbose", "--target-pane", "%123", "foobar", "m1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildChildArgs(...) = %#v; want %#v", got, want)
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

func TestFormatIdleDifferences(t *testing.T) {
	got := formatIdleDifferences([]int{0, 2, 0, 4}, []int{0, 1, 0, 3})
	want := "differences relative to sample 1: sample 2: base=2 prev=1, sample 4: base=4 prev=3"
	if got != want {
		t.Fatalf("formatIdleDifferences(...) = %q; want %q", got, want)
	}
}
