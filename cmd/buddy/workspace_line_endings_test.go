package main

import "testing"

func TestWorkspaceEditsPreserveLineEndings(t *testing.T) {
	for _, tc := range []struct{ original, edited, want string }{
		{"a\nb\n", "a\nB\n", "a\nB\n"},
		{"a\r\nb\r\n", "a\nB\n", "a\r\nB\r\n"},
		{"a\r\nb\nc\r\n", "a\nB\nc\n", "a\r\nB\nc\r\n"},
		{"a\r\nb\nc\r\n", "a\nnew\nb\nc\n", "a\r\nnew\r\nb\nc\r\n"},
		{"a\r\nb\nc\r\n", "b\nc\n", "b\nc\r\n"},
		{"a\r\nb\nc\r\n", "a\nb\nc", "a\r\nb\nc"},
		{"a\r\nb\nc", "a\nb\nc\n", "a\r\nb\nc\n"},
		{"a\r\nb\nc\r\n", "", ""},
	} {
		if got := workspaceEditedText(tc.original, tc.edited); got != tc.want {
			t.Errorf("editing %q to %q gave %q, want %q", tc.original, tc.edited, got, tc.want)
		}
	}
}
