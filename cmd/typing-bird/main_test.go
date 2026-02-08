package main

import (
	"reflect"
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
