package preprocess

import (
	"context"
	"io"
	"regexp"
	"strings"
)

var (
	reFrontmatter = regexp.MustCompile(`(?s)^---\n.*?\n---\n?`)
	reCodeFence   = regexp.MustCompile("(?m)^```[^\n]*\n?")
	reBold        = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reItalicUS    = regexp.MustCompile(`_(.+?)_`)
	reInlineCode  = regexp.MustCompile("`(.+?)`")
	reHeading     = regexp.MustCompile(`(?m)^#{1,6}\s+`)
)

type markdownExtractor struct{}

func (e *markdownExtractor) Extract(_ context.Context, r io.Reader) (string, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	s := string(b)

	s = reFrontmatter.ReplaceAllString(s, "")
	s = reCodeFence.ReplaceAllString(s, "")
	s = reBold.ReplaceAllString(s, "$1")
	s = reItalicUS.ReplaceAllString(s, "$1")
	s = reInlineCode.ReplaceAllString(s, "$1")
	s = reHeading.ReplaceAllString(s, "")

	return strings.TrimSpace(s), nil
}
