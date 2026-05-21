package parse

import (
	"reflect"
	"testing"
)

func TestExtractIDs(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"<a@x>", []string{"a@x"}},
		{" <a@x> <b@y> ", []string{"a@x", "b@y"}},
		{"<a@x>\n<b@y>", []string{"a@x", "b@y"}},
		{"bare@id", []string{"bare@id"}},
	}
	for _, tt := range tests {
		got := ExtractIDs(tt.in)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("ExtractIDs(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestNormalizeSubject(t *testing.T) {
	tests := map[string]string{
		"":                   "",
		"Hello":              "hello",
		"Re: Hello":          "hello",
		"RE: re: Hello":      "hello",
		"Fwd: Re: Hello":     "hello",
		"  Re:   Hello  ":    "hello",
		"FW: Hello World":    "hello world",
	}
	for in, want := range tests {
		if got := NormalizeSubject(in); got != want {
			t.Errorf("NormalizeSubject(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestThreadCandidates(t *testing.T) {
	got := ThreadCandidates("<self@x>", "<parent@y>", "<root@z> <parent@y>")
	// Want: root first, then parent, then self; no duplicates.
	want := []string{"<root@z>"[1 : len("<root@z>")-1], "<parent@y>"[1 : len("<parent@y>")-1], "<self@x>"[1 : len("<self@x>")-1]}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ThreadCandidates = %v, want %v", got, want)
	}

	// No headers except message-id -> just self.
	got2 := ThreadCandidates("self@x", "", "")
	if !reflect.DeepEqual(got2, []string{"self@x"}) {
		t.Errorf("ThreadCandidates self-only = %v", got2)
	}
}
