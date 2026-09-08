package eval

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Set is a labelled evaluation set: a corpus-shaped question paper that the
// retriever and the model are marked against.
type Set struct {
	Version     int    `yaml:"version"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Cases       []Case `yaml:"cases"`
}

// Case is one question, with whatever is known about the right answer to it.
//
// The stages read different halves: retrieval scores Query (or the queries a
// planner made of it, with Thread behind it) against Relevant, and generation
// scores the completion against Answer and MustInclude. A case may carry either
// half, or both; it may not carry neither.
type Case struct {
	ID    string `yaml:"id"`
	Query string `yaml:"query"`
	// GoldQuery is the standalone question a competent human would have typed
	// in place of Query — the ceiling a rewrite is trying to reach. Retrieving
	// on it is an ordinary search, so the ceiling costs no model call, which is
	// what makes it worth a column of its own.
	GoldQuery string `yaml:"gold_query"`
	// Thread is what was asked before Query, oldest first. Without it a
	// follow-up like "and maps?" is unmeasurable: every rewrite mode is an
	// identity on a question with nothing behind it.
	Thread   []Turn  `yaml:"thread"`
	Relevant []Label `yaml:"relevant"`
	// Answer is the reference the generation stage marks a completion against.
	Answer      string   `yaml:"answer"`
	MustInclude []string `yaml:"must_include"`
}

// Turn is one exchange behind the question, replayed to the planner exactly as
// a stored thread would be — and, unlike a stored thread, never persisted.
type Turn struct {
	Question string `yaml:"question"`
	Answer   string `yaml:"answer"`
}

// Label names a passage that ought to come back for a case.
//
// Path is matched as a suffix (see MatchesPath) so a set stays portable across
// machines and platforms; Contains, when given, pins which passage of that
// document was meant. Grade is graded relevance for nDCG and defaults to 1.
type Label struct {
	Path     string `yaml:"path"`
	Contains string `yaml:"contains"`
	Grade    int    `yaml:"grade"`
}

// LoadSet reads a label set from a file. An unnamed set takes the file's name,
// so a report is self-describing and --baseline has an identity to compare.
func LoadSet(path string) (Set, error) {
	f, err := os.Open(path) //nolint:gosec // the path is the user's own argument
	if err != nil {
		return Set{}, fmt.Errorf("eval: open label set: %w", err)
	}
	defer func() { _ = f.Close() }()

	set, err := ParseSet(f)
	if err != nil {
		return Set{}, fmt.Errorf("%s: %w", path, err)
	}
	if set.Name == "" {
		base := filepath.Base(path)
		set.Name = strings.TrimSuffix(base, filepath.Ext(base))
	}
	return set, nil
}

// ParseSet decodes and validates a label set.
//
// Unknown keys are an error. A typo in a key would otherwise leave a set that
// parses, runs, and quietly measures something other than what was written
// down — the one failure an eval harness cannot afford, because its output
// looks identical either way.
func ParseSet(r io.Reader) (Set, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)

	var set Set
	if err := dec.Decode(&set); err != nil {
		if errors.Is(err, io.EOF) {
			return Set{}, errors.New("eval: parse label set: the file is empty")
		}
		return Set{}, fmt.Errorf("eval: parse label set: %w", err)
	}
	set.trim()
	if err := set.Validate(); err != nil {
		return Set{}, err
	}
	set.applyDefaults()
	return set, nil
}

// Validate reports the first structural problem with the set, or nil.
//
// Everything it rejects is rejected before a single search runs: a set that
// fails on its twentieth case after nineteen model calls has spent them for
// nothing.
func (s Set) Validate() error {
	switch {
	case s.Version == 0:
		return fmt.Errorf("eval: label set has no version (add `version: %d` at the top)", Version)
	case s.Version != Version:
		return fmt.Errorf("eval: label set version %d is not supported; this build reads version %d",
			s.Version, Version)
	}
	if len(s.Cases) == 0 {
		return fmt.Errorf("eval: label set %q has no cases", s.Name)
	}

	seen := make(map[string]bool, len(s.Cases))
	for i, c := range s.Cases {
		where := fmt.Sprintf("case %d", i+1)
		if c.ID != "" {
			where = fmt.Sprintf("case %q", c.ID)
		}
		if err := c.validate(where); err != nil {
			return err
		}
		if seen[c.ID] {
			return fmt.Errorf("eval: duplicate case id %q; ids name a row in the report and have to be unique", c.ID)
		}
		seen[c.ID] = true
	}
	return nil
}

func (c Case) validate(where string) error {
	if c.ID == "" {
		return fmt.Errorf("eval: %s has no id", where)
	}
	if c.Query == "" {
		return fmt.Errorf("eval: %s has no query", where)
	}
	for i, t := range c.Thread {
		if t.Question == "" {
			return fmt.Errorf("eval: %s: thread turn %d has no question", where, i+1)
		}
	}
	for i, l := range c.Relevant {
		if l.Path == "" {
			return fmt.Errorf("eval: %s: relevant entry %d has no path", where, i+1)
		}
		if l.Grade < 0 {
			return fmt.Errorf("eval: %s: relevant entry %d has grade %d; a grade must not be negative",
				where, i+1, l.Grade)
		}
	}
	// A case no stage can score dilutes every macro-average it sits in, and
	// does it silently — the report shows a row of zeros that reads exactly
	// like a retrieval failure.
	if len(c.Relevant) == 0 && c.Answer == "" && len(c.MustInclude) == 0 {
		return fmt.Errorf("eval: %s has nothing to score: give it relevant paths, an answer, or must_include", where)
	}
	return nil
}

// trim strips the whitespace YAML block scalars and hand editing leave behind,
// so a trailing newline in `answer:` does not fail a must_include comparison or
// print a blank line into every report.
func (s *Set) trim() {
	s.Name = strings.TrimSpace(s.Name)
	s.Description = strings.TrimSpace(s.Description)
	for i := range s.Cases {
		c := &s.Cases[i]
		c.ID = strings.TrimSpace(c.ID)
		c.Query = strings.TrimSpace(c.Query)
		c.GoldQuery = strings.TrimSpace(c.GoldQuery)
		c.Answer = strings.TrimSpace(c.Answer)
		for j := range c.MustInclude {
			c.MustInclude[j] = strings.TrimSpace(c.MustInclude[j])
		}
		for j := range c.Thread {
			c.Thread[j].Question = strings.TrimSpace(c.Thread[j].Question)
			c.Thread[j].Answer = strings.TrimSpace(c.Thread[j].Answer)
		}
		for j := range c.Relevant {
			c.Relevant[j].Path = strings.TrimSpace(c.Relevant[j].Path)
			c.Relevant[j].Contains = strings.TrimSpace(c.Relevant[j].Contains)
		}
	}
}

// applyDefaults fills the grade every unlabelled label has. Run after
// validation, so an explicit negative grade is still an error rather than
// something quietly replaced by the default.
func (s *Set) applyDefaults() {
	for i := range s.Cases {
		for j := range s.Cases[i].Relevant {
			if s.Cases[i].Relevant[j].Grade == 0 {
				s.Cases[i].Relevant[j].Grade = 1
			}
		}
	}
}
