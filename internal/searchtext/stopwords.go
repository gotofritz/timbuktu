package searchtext

import "strings"

// baseStopWords are the words a code region is made of rather than about:
// control flow, declarations, visibility, primitive types. They appear in every
// file in every language, so they say nothing about which chunk is wanted, and
// a chunk indexed as "func return if else int" is indexed as noise.
//
// The list is deliberately short. A word that could be the subject of a note —
// map, log, index, table — is left in unless a language's own list takes it,
// because dropping it loses exactly the signal this encoding exists to keep.
var baseStopWords = words(`
	abstract and as async await begin bool boolean break byte case catch char
	class const continue def default delete do double elif else end enum except
	extends extern false final finally float fn for func function goto if
	implements import in include int interface is lambda let long new
	nil none not null or override package pass private protected public raise
	return short static str string struct super switch then this throw throws
	true try type typedef typename union unsigned use using val var virtual void
	while with yield
`)

// languageStopWords are the extra words a fence's language tag buys. Nothing
// depends on the tag being there — an untagged block just gets the base list.
var languageStopWords = map[string][]string{
	"go":         words(`append cap chan defer go iota len make map range select`),
	"python":     words(`assert del elif global nonlocal print self`),
	"javascript": words(`console document export function instanceof require typeof undefined window`),
	"sql":        words(`alter by create distinct drop from group having index insert into join limit on order select set table update values where`),
	"shell":      words(`done echo esac fi local read`),
	"c":          words(`define endif ifdef ifndef include printf sizeof`),
}

// languageAliases maps the tags people actually write on a fence to the lists
// above.
var languageAliases = map[string]string{
	"bash": "shell", "sh": "shell", "zsh": "shell", "console": "shell",
	"js": "javascript", "jsx": "javascript", "ts": "javascript",
	"tsx": "javascript", "typescript": "javascript", "node": "javascript",
	"py": "python", "python3": "python",
	"golang": "go",
	"psql":   "sql", "sqlite": "sql", "mysql": "sql", "postgres": "sql", "postgresql": "sql",
	"c++": "c", "cpp": "c", "cc": "c", "h": "c", "objc": "c",
}

// stopWordsFor returns the words to drop from a region tagged lang. The result
// is read-only and shared, so callers must not write to it.
func stopWordsFor(lang string) map[string]bool {
	if lang == "" {
		return baseStop
	}
	if alias, ok := languageAliases[lang]; ok {
		lang = alias
	}
	if set, ok := langStop[lang]; ok {
		return set
	}
	return baseStop
}

// baseStop and langStop are the lists above as lookup sets, built once at
// package load so a reduce does not rebuild them per fence. Each language set
// includes the base words.
var (
	baseStop = set(baseStopWords)
	langStop = buildLangStop()
)

func buildLangStop() map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(languageStopWords))
	for lang, extra := range languageStopWords {
		s := set(baseStopWords)
		for _, w := range extra {
			s[w] = true
		}
		out[lang] = s
	}
	return out
}

func words(s string) []string { return strings.Fields(s) }

func set(list []string) map[string]bool {
	s := make(map[string]bool, len(list))
	for _, w := range list {
		s[w] = true
	}
	return s
}
