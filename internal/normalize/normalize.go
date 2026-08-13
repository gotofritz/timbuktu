// Package normalize repairs model output that has drifted from the shape a
// prompt template asked for. Long generations reliably slide back towards
// markdown habits — bullets return, separators stop being emitted — and no
// amount of prompt wording fixes it, so a template declares the shape it needs
// and gets it enforced after the fact.
//
// A template opts in from its manifest:
//
//	normalize:
//	  filters: [strip_preamble, strip_fences, strip_list_markers]
//	  records:
//	    separator: "----"
//	    fields: [lead, note, body]
//
// Filters are line-level cleanups applied in the order listed; the optional
// records block rebuilds the output as separator-delimited records. Templates
// that declare nothing are left untouched.
package normalize

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Config is a template's declared normalization pipeline.
type Config struct {
	Filters []string       `yaml:"filters"`
	Records *RecordsConfig `yaml:"records"`
}

// Field names a positional line role inside a record.
const (
	// FieldLead is the record's first line — a question, a term, a heading:
	// whatever the template's first field means.
	FieldLead = "lead"
	// FieldNote is an optional parenthesised second line. It is positional, so
	// it is emitted even when empty: dropping it would shift the first body
	// line into the note.
	FieldNote = "note"
	// FieldBody is everything after, one item per line, blank lines removed.
	FieldBody = "body"
)

// RecordsConfig rebuilds output as records separated by Separator. Fields lists
// the record's line roles in order: it starts with lead, ends with body, and may
// carry note in between. An empty Fields means [lead, body].
type RecordsConfig struct {
	Separator string   `yaml:"separator"`
	Fields    []string `yaml:"fields"`
}

// fields returns the configured roles, defaulted.
func (r RecordsConfig) fields() []string {
	if len(r.Fields) == 0 {
		return []string{FieldLead, FieldBody}
	}
	return r.Fields
}

// hasNote reports whether records reserve the positional note line.
func (r RecordsConfig) hasNote() bool {
	for _, f := range r.fields() {
		if f == FieldNote {
			return true
		}
	}
	return false
}

// Declared reports whether the template asked for any normalization.
func (c Config) Declared() bool { return len(c.Filters) > 0 || c.Records != nil }

// Validate reports unknown filter names and unusable card settings, so a broken
// manifest fails when the template loads rather than after a model call.
func (c Config) Validate() error {
	for _, name := range c.Filters {
		if _, ok := filters[name]; !ok {
			return fmt.Errorf("unknown normalize filter %q (available: %s)", name, strings.Join(FilterNames(), ", "))
		}
	}
	if c.Records == nil {
		return nil
	}
	if strings.TrimSpace(c.Records.Separator) == "" {
		return fmt.Errorf("normalize records: separator must not be empty")
	}
	return validateFields(c.Records.fields())
}

// validateFields enforces the one shape the assembler can build: a lead line,
// an optional note line, then the body.
func validateFields(fields []string) error {
	for _, f := range fields {
		if f != FieldLead && f != FieldNote && f != FieldBody {
			return fmt.Errorf("normalize records: unknown field %q (available: %s, %s, %s)",
				f, FieldLead, FieldNote, FieldBody)
		}
	}
	if len(fields) < 2 || fields[0] != FieldLead || fields[len(fields)-1] != FieldBody {
		return fmt.Errorf("normalize records: fields must start with %s and end with %s, got %v",
			FieldLead, FieldBody, fields)
	}
	for _, f := range fields[1 : len(fields)-1] {
		if f != FieldNote {
			return fmt.Errorf("normalize records: only %s may sit between %s and %s, got %q",
				FieldNote, FieldLead, FieldBody, f)
		}
	}
	return nil
}

// Apply runs the pipeline over raw model output. An undeclared pipeline returns
// raw unchanged.
func Apply(raw string, cfg Config) (string, error) {
	if !cfg.Declared() {
		return raw, nil
	}
	if err := cfg.Validate(); err != nil {
		return "", err
	}

	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	for _, name := range cfg.Filters {
		lines = filters[name](lines)
	}
	if cfg.Records == nil {
		return joinNonEmpty(lines), nil
	}
	return buildRecords(lines, *cfg.Records), nil
}

// FilterNames lists every registered filter, sorted, for manifests and error
// messages.
func FilterNames() []string {
	names := make([]string, 0, len(filters))
	for name := range filters {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// A filter rewrites the line sequence; dropping lines is allowed.
type filter func([]string) []string

var (
	listMarkerRe = regexp.MustCompile(`^\s*(?:[-*+•‣▪]|\d+[.)])\s+`)
	headingRe    = regexp.MustCompile(`^\s*#{1,6}\s+`)
	separatorRe  = regexp.MustCompile(`^\s*(?:-{3,}|={3,}|_{3,}|\*{3,})\s*$`)
	noteRe       = regexp.MustCompile(`^\s*\(.*\)\s*$`)

	filters = map[string]filter{
		"strip_fences":         stripFences,
		"strip_headings":       stripHeadings,
		"strip_list_markers":   stripListMarkers,
		"strip_preamble":       stripPreamble,
		"collapse_blank_lines": collapseBlankLines,
		"trim_trailing_space":  trimTrailingSpace,
	}
)

// stripFences drops code-fence lines, keeping the fenced content.
func stripFences(lines []string) []string {
	out := lines[:0:0]
	for _, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), "```") {
			continue
		}
		out = append(out, ln)
	}
	return out
}

// stripHeadings drops markdown heading lines.
func stripHeadings(lines []string) []string {
	out := lines[:0:0]
	for _, ln := range lines {
		if headingRe.MatchString(ln) {
			continue
		}
		out = append(out, ln)
	}
	return out
}

// stripListMarkers removes a leading bullet or numbering from each line,
// flattening nested items. Lines that merely continue an item keep their
// indentation.
func stripListMarkers(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		out = append(out, listMarkerRe.ReplaceAllString(ln, ""))
	}
	return out
}

// stripPreamble drops an opening line such as "Here are the flashcards:" — a
// single line ending in a colon, before the first blank line.
func stripPreamble(lines []string) []string {
	for i, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if trimmed == "" {
			continue
		}
		if strings.HasSuffix(trimmed, ":") && i+1 < len(lines) && strings.TrimSpace(lines[i+1]) == "" {
			return lines[i+1:]
		}
		return lines
	}
	return lines
}

// collapseBlankLines squeezes runs of blank lines into one.
func collapseBlankLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	prevBlank := false
	for _, ln := range lines {
		blank := strings.TrimSpace(ln) == ""
		if blank && prevBlank {
			continue
		}
		out = append(out, ln)
		prevBlank = blank
	}
	return out
}

func trimTrailingSpace(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		out = append(out, strings.TrimRight(ln, " \t"))
	}
	return out
}

// record is one assembled record: a lead line, an optional note, and body lines.
type record struct {
	lead string
	note string
	body []string
}

// buildRecords rebuilds lines into separator-delimited records. Explicit
// separator lines start a record; where the model stopped emitting them, a lead
// line following a blank line starts the next one — recognised by a trailing
// question mark, the marker templates of this shape use. Output with no question
// marks at all falls back to blank-line-delimited blocks.
func buildRecords(lines []string, cfg RecordsConfig) string {
	questionMode := containsQuestion(lines)
	noteField := cfg.hasNote()
	// Chatter before the first record — "Here are the cards.", "Sure thing" — is
	// not a lead line. When leads are recognisable (they end in a question mark)
	// anything ahead of the first one, or ahead of the first separator, is
	// dropped. With no question marks anywhere there is nothing to tell chatter
	// from a legitimate first lead, so the input is left as it is.
	if questionMode {
		lines = dropPreamble(lines, cfg)
	}
	sep := strings.TrimSpace(cfg.Separator)
	// A template whose separator is a markdown rule also accepts the neighbouring
	// rules: a model asked for "----" writes "---" or "-----" often enough. One
	// that picked something else splits on that alone, so a rule sitting in the
	// body is left as content.
	rulesToo := separatorRe.MatchString(sep)

	var records []record
	blank := true // the start of the input behaves like a boundary
	for _, ln := range lines {
		text := strings.TrimRight(ln, " \t")
		switch {
		case strings.TrimSpace(text) == sep || (rulesToo && separatorRe.MatchString(text)):
			records = closeEmpty(records)
			blank = true
			continue
		case strings.TrimSpace(text) == "":
			blank = true
			continue
		}

		switch {
		case len(records) == 0 || (blank && startsRecord(records[len(records)-1], text, questionMode)):
			records = append(records, record{lead: text})
		case noteField && records[len(records)-1].note == "" &&
			len(records[len(records)-1].body) == 0 && noteRe.MatchString(text):
			records[len(records)-1].note = strings.TrimSpace(text)
		default:
			last := &records[len(records)-1]
			last.body = append(last.body, text)
		}
		blank = false
	}

	var sb strings.Builder
	for _, r := range records {
		if len(r.body) == 0 {
			continue // a lead line with no body is not a record
		}
		sb.WriteString(cfg.Separator)
		sb.WriteString("\n\n")
		sb.WriteString(r.lead)
		sb.WriteString("\n")
		// Without a note field the same blank line separates lead from body.
		sb.WriteString(r.note)
		sb.WriteString("\n")
		sb.WriteString(strings.Join(r.body, "\n"))
		sb.WriteString("\n\n")
	}
	return strings.TrimSuffix(sb.String(), "\n")
}

// dropPreamble discards the lines before the first separator or question line.
func dropPreamble(lines []string, cfg RecordsConfig) []string {
	sep := strings.TrimSpace(cfg.Separator)
	rulesToo := separatorRe.MatchString(sep)
	for i, ln := range lines {
		text := strings.TrimSpace(ln)
		if text == "" {
			continue
		}
		if text == sep || (rulesToo && separatorRe.MatchString(ln)) || strings.HasSuffix(text, "?") {
			return lines[i:]
		}
	}
	return lines
}

// startsRecord reports whether text opens a new record rather than continuing
// the current one. It is only consulted after a blank line.
func startsRecord(current record, text string, questionMode bool) bool {
	if len(current.body) == 0 {
		return false // still filling the open record's body
	}
	if !questionMode {
		return true // blank lines are the only boundary available
	}
	return strings.HasSuffix(strings.TrimSpace(text), "?")
}

// closeEmpty drops a trailing record that never received a body, so a stray
// separator does not strand the lead line that preceded it.
func closeEmpty(records []record) []record {
	if n := len(records); n > 0 && len(records[n-1].body) == 0 {
		return records[:n-1]
	}
	return records
}

func containsQuestion(lines []string) bool {
	for _, ln := range lines {
		if strings.HasSuffix(strings.TrimSpace(ln), "?") {
			return true
		}
	}
	return false
}

// joinNonEmpty renders filtered lines, dropping leading and trailing blanks.
func joinNonEmpty(lines []string) string {
	start, end := 0, len(lines)
	for start < end && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	for end > start && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	if start == end {
		return ""
	}
	return strings.Join(lines[start:end], "\n") + "\n"
}
