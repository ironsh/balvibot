package parse

import (
	"regexp"
	"strings"
)

var (
	angle      = regexp.MustCompile(`<([^>]+)>`)
	subjectPfx = regexp.MustCompile(`(?i)^(re|fwd?|aw|sv|tr)\s*:\s*`)
	whitespace = regexp.MustCompile(`\s+`)
)

// ExtractIDs pulls Message-IDs out of a raw header value (RFC 5322 angle-bracket form).
// Returns IDs in order. Bare tokens without angle brackets are accepted as a fallback.
func ExtractIDs(s string) []string {
	if s == "" {
		return nil
	}
	out := []string{}
	for _, m := range angle.FindAllStringSubmatch(s, -1) {
		v := strings.TrimSpace(m[1])
		if v != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		for _, tok := range strings.Fields(s) {
			tok = strings.Trim(tok, "<>")
			if tok != "" && strings.Contains(tok, "@") {
				out = append(out, tok)
			}
		}
	}
	return out
}

// NormalizeSubject strips repeated Re:/Fwd: prefixes and collapses whitespace.
// The result is lowercased for case-insensitive matching.
func NormalizeSubject(s string) string {
	s = strings.TrimSpace(s)
	for {
		new := subjectPfx.ReplaceAllString(s, "")
		if new == s {
			break
		}
		s = new
	}
	s = whitespace.ReplaceAllString(s, " ")
	return strings.ToLower(strings.TrimSpace(s))
}

// ThreadCandidates returns the ordered list of Message-IDs that may identify
// this message's thread. The first element is the preferred root (leftmost of
// References, falling back to In-Reply-To, then this message's own Message-ID).
func ThreadCandidates(messageID, inReplyTo, references string) []string {
	var cands []string
	refs := ExtractIDs(references)
	if len(refs) > 0 {
		cands = append(cands, refs[0])
	}
	if irt := ExtractIDs(inReplyTo); len(irt) > 0 {
		cands = append(cands, irt[0])
	}
	cands = append(cands, refs...)
	cands = append(cands, ExtractIDs(inReplyTo)...)
	if messageID != "" {
		cands = append(cands, strings.Trim(messageID, "<> "))
	}

	seen := map[string]bool{}
	uniq := cands[:0]
	for _, c := range cands {
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		uniq = append(uniq, c)
	}
	return uniq
}
