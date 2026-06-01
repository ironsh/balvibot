package store

import (
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestCleanText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain ascii", "hello world", "hello world"},
		{"valid utf8 preserved", "héllo — 世界", "héllo — 世界"},
		{"strips nul bytes", "ab\x00cd", "abcd"},
		{"replaces lone continuation byte", "ab\x89cd", "ab�cd"},
		{"replaces png header bytes", "\x89PNG\r\n", "�PNG\r\n"},
		{"strips nul and replaces invalid", "a\x00b\xffc", "ab�c"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cleanText(tc.in)
			require.Equal(t, tc.want, got)
			require.True(t, utf8.ValidString(got), "result must be valid UTF-8")
			require.NotContains(t, got, "\x00", "result must not contain NUL")
		})
	}
}
