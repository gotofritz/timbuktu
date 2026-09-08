package eval

import "strings"

// MatchesPath reports whether docPath — a document path as the knowledge base
// stores it, absolute and platform-shaped — is the document labelPath names.
//
// A label is written by hand and has to stay portable, so it names a suffix:
// `go/slices.md` matches /home/u/notes/go/slices.md and D:\notes\go\slices.md
// alike. The suffix has to begin on a separator boundary, so `slices.md` does
// not match go-slices.md — without that rule a label would silently credit a
// neighbouring document, and the error would flatter the retriever.
//
// Case is folded and separators are levelled. The same corpus indexed on
// Windows and on Linux has to score the same, or a set is worthless for the
// comparison it exists to make.
func MatchesPath(labelPath, docPath string) bool {
	label := strings.TrimPrefix(normPath(labelPath), "/")
	doc := normPath(docPath)
	if label == "" || doc == "" {
		return false
	}
	return doc == label || strings.HasSuffix(doc, "/"+label)
}

// MatchesText reports whether text contains anchor, ignoring case and reading
// any run of whitespace as a single space.
//
// A chunk boundary that reflows, or a preprocessor that collapsed a line break,
// must not turn a passing case into a failing one: that is noise in the
// measuring instrument, and noise there is indistinguishable from the
// regression the instrument exists to find. Nothing else is normalised —
// punctuation is signal, so `len == cap` does not match `len==cap`.
//
// An empty anchor is satisfied by any text, which is what a label with no
// `contains` means: the document alone is the label.
func MatchesText(anchor, text string) bool {
	a := collapseSpace(anchor)
	if a == "" {
		return true
	}
	return strings.Contains(collapseSpace(text), a)
}

// Matches reports whether a retrieved chunk satisfies the label.
func (l Label) Matches(r Result) bool {
	return MatchesPath(l.Path, r.Path) && MatchesText(l.Contains, r.Text)
}

// PathIssue is a label whose path does not resolve to exactly one indexed
// document, and what it did resolve to.
type PathIssue struct {
	CaseID  string
	Path    string   // the label path, as written
	Matches []string // indexed documents it matched: none, or more than one
}

// CheckPaths resolves every label path against the documents actually in the
// index, reporting those that match nothing and those that match more than one.
//
// Both are worth saying out loud and neither is fatal on its own. A label
// naming a document that was never ingested scores zero forever and reads on
// the report exactly like a retrieval failure; an ambiguous one would be scored
// against whichever document happened to sort first, which is how a harness
// quietly starts lying.
func (s Set) CheckPaths(indexed []string) (missing, ambiguous []PathIssue) {
	// One document spelled two ways is one document, not two candidates.
	byNorm := make(map[string]string, len(indexed))
	order := make([]string, 0, len(indexed))
	for _, p := range indexed {
		n := normPath(p)
		if n == "" {
			continue
		}
		if _, ok := byNorm[n]; ok {
			continue
		}
		byNorm[n] = p
		order = append(order, n)
	}

	for _, c := range s.Cases {
		for _, l := range c.Relevant {
			var matches []string
			for _, n := range order {
				if MatchesPath(l.Path, n) {
					matches = append(matches, byNorm[n])
				}
			}
			issue := PathIssue{CaseID: c.ID, Path: l.Path, Matches: matches}
			switch {
			case len(matches) == 0:
				missing = append(missing, issue)
			case len(matches) > 1:
				ambiguous = append(ambiguous, issue)
			}
		}
	}
	return missing, ambiguous
}

// normPath levels the two things that differ between machines but never
// between documents: the separator and the case.
func normPath(p string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(p), `\`, "/"))
}

// collapseSpace lowercases and reduces every run of whitespace to one space.
func collapseSpace(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}
