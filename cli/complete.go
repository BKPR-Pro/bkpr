package main

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/BKPR-Pro/bkpr/lib/books"
	"github.com/BKPR-Pro/bkpr/lib/store"
)

// Shell completion is computed in Go and forwarded through a hidden `__complete` command, so the
// per-shell scripts stay trivial: they hand the words typed so far to `bkpr __complete` and feed the
// candidates it prints back to their completion system. The candidates themselves come from the same
// usageSections table that renders `--help`, so the two can never drift, and connector names are read
// live from the book of record.

// compDirective is the trailing hint `__complete` prints after its candidates, telling the shell
// whether to fall back to file-name completion. It mirrors the small slice of shell-completion
// directives this tool needs; the value is the bit the scripts test for.
type compDirective int

const (
	compDefault compDirective = 0 // the shell may also offer file names (e.g. import takes a file)
	compNoFiles compDirective = 4 // the candidates are the whole answer; do not offer file names
)

var (
	flagRe        = regexp.MustCompile(`-[a-zA-Z][a-zA-Z0-9-]*`)
	placeholderRe = regexp.MustCompile(`<[^>]*>`)
)

// cmdModel is the command grammar completion reads, derived once from usageSections: the top-level
// verbs, the subcommands under each, and the flags every command path accepts. Because it is folded
// out of the same table that prints the usage screen, a command or flag added to the help is
// completable with no second edit.
type cmdModel struct {
	top   []string            // sorted top-level command names
	subs  map[string][]string // parent verb -> its sorted subcommands
	flags map[string][]string // command path (e.g. "rules set") -> its sorted flags
}

// buildModel folds usageSections into the completion grammar. A verb is one or two words: the first
// is the top-level command, the second (when present) a subcommand. Flags are the -tokens in the
// synopsis once the <placeholders> are stripped, unioned across a command's several synopsis lines
// (import has one per input kind), so completing a command's flags offers all of them.
func buildModel() cmdModel {
	m := cmdModel{subs: map[string][]string{}, flags: map[string][]string{}}
	topSet := map[string]bool{}
	subSets := map[string]map[string]bool{}
	flagSets := map[string]map[string]bool{}
	for _, s := range usageSections {
		for _, l := range s.lines {
			toks := strings.Fields(l.verb)
			if len(toks) == 0 {
				continue
			}
			topSet[toks[0]] = true
			if len(toks) == 2 {
				if subSets[toks[0]] == nil {
					subSets[toks[0]] = map[string]bool{}
				}
				subSets[toks[0]][toks[1]] = true
			}
			if flagSets[l.verb] == nil {
				flagSets[l.verb] = map[string]bool{}
			}
			for _, f := range flagRe.FindAllString(placeholderRe.ReplaceAllString(l.args, ""), -1) {
				flagSets[l.verb][f] = true
			}
		}
	}
	m.top = sortedKeys(topSet)
	for parent, set := range subSets {
		m.subs[parent] = sortedKeys(set)
	}
	for path, set := range flagSets {
		m.flags[path] = sortedKeys(set)
	}
	return m
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func contains(xs []string, x string) bool {
	for _, s := range xs {
		if s == x {
			return true
		}
	}
	return false
}

// completeArgs answers what could come next. words is the command line after "bkpr", its last
// element the word under the cursor (empty when a fresh word is being started); everything before it
// is already committed. connectors is read lazily -- only where a connector name is the thing being
// typed -- so completing a command never opens the book. Candidates are returned unfiltered by
// prefix; the shell narrows them to what the user has typed.
func completeArgs(words []string, connectors func() []string) ([]string, compDirective) {
	m := buildModel()
	if len(words) == 0 {
		words = []string{""}
	}
	partial := words[len(words)-1]
	committed := words[:len(words)-1]

	// Resolve the command path: one word, or two when the first names a subcommand-bearing verb and
	// the second is one of its subcommands.
	var path string
	pathLen := 0
	if len(committed) >= 1 && !strings.HasPrefix(committed[0], "-") {
		path, pathLen = committed[0], 1
		if subs, ok := m.subs[committed[0]]; ok {
			if len(committed) >= 2 && !strings.HasPrefix(committed[1], "-") && contains(subs, committed[1]) {
				path, pathLen = committed[0]+" "+committed[1], 2
			}
		}
	}

	// A flag is being typed: offer the command's flags (none, and so nothing, at the top level).
	if strings.HasPrefix(partial, "-") {
		if path == "" {
			return nil, compNoFiles
		}
		return m.flags[path], compNoFiles
	}

	// No command yet: the top-level verbs.
	if path == "" {
		return m.top, compNoFiles
	}

	// A subcommand-bearing verb still waiting for its subcommand.
	if subs, ok := m.subs[path]; ok && pathLen == 1 {
		return subs, compNoFiles
	}

	// The first argument after a resolved command path. Only this position is completed dynamically;
	// once more is typed the shape is command-specific and left to the shell (and to flags, above).
	if len(committed) == pathLen {
		return firstArgCompletions(path, m, connectors)
	}
	return nil, compNoFiles
}

// firstArgCompletions completes the argument a command takes first, where that argument is a name
// this tool knows how to enumerate: a connector for the verbs that read or write one, a command name
// for help, a shell for completion. import also accepts a file there, so it keeps file completion on;
// the rest do not name a file and turn it off.
func firstArgCompletions(path string, m cmdModel, connectors func() []string) ([]string, compDirective) {
	switch path {
	case "import":
		return connectors(), compDefault
	case "export", "connectors rm":
		return connectors(), compNoFiles
	case "help":
		return m.top, compNoFiles
	case "completion":
		return []string{"bash", "zsh", "fish"}, compNoFiles
	}
	return nil, compNoFiles
}

// runComplete is the hidden `__complete` command: it prints the candidates one per line and, last, a
// :N directive line the shell reads to decide on file completion. It never errors or exits nonzero --
// a shell calls it on every keystroke -- so a book that will not open simply yields no connectors.
func runComplete(w io.Writer, words []string, connectors func() []string) {
	cands, dir := completeArgs(words, connectors)
	for _, c := range cands {
		fmt.Fprintln(w, c)
	}
	fmt.Fprintf(w, ":%d\n", int(dir))
}

// liveConnectors reads the registered connectors' names from the nearest book, or none when there is
// no book here or it will not open. It is the connector source `__complete` runs in earnest.
func liveConnectors() []string {
	s, err := store.OpenReader(".")
	if err != nil {
		return nil
	}
	defer s.Close()
	set, err := books.Connectors(s.Log)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(set))
	for _, c := range set {
		names = append(names, c.Name)
	}
	return names
}

// completionCmd prints the shell integration script for `bkpr completion <shell>`, to be sourced
// (e.g. eval "$(bkpr completion zsh)") so the shell learns to call `__complete`.
func completionCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("completion needs a shell: bash, zsh, or fish")
	}
	return completionScript(os.Stdout, args[0])
}

// completionScript writes the integration script for one shell. Each script is a thin forwarder: it
// gathers the words typed so far, calls `bkpr __complete`, strips the trailing :N directive, and
// hands the rest to the shell's completion machinery, honoring the directive for file completion.
func completionScript(w io.Writer, shell string) error {
	switch shell {
	case "bash":
		fmt.Fprint(w, bashCompletion)
	case "zsh":
		fmt.Fprint(w, zshCompletion)
	case "fish":
		fmt.Fprint(w, fishCompletion)
	default:
		return fmt.Errorf("completion: unknown shell %q; bash, zsh, or fish", shell)
	}
	return nil
}

const bashCompletion = `# bash completion for bkpr; source with: eval "$(bkpr completion bash)"
_bkpr() {
    local cur="${COMP_WORDS[COMP_CWORD]}"
    local args=("${COMP_WORDS[@]:1:COMP_CWORD-1}")
    args+=("$cur")
    local out directive cands
    out="$(bkpr __complete "${args[@]}" 2>/dev/null)"
    directive="${out##*$'\n'}"
    if [[ "$directive" == :* ]]; then
        cands="${out%$'\n'*}"
        [[ "$cands" == "$directive" ]] && cands=""
    else
        cands="$out"
        directive=":0"
    fi
    COMPREPLY=($(compgen -W "$cands" -- "$cur"))
    if [[ "$directive" == *4* ]]; then
        compopt +o default 2>/dev/null
    fi
}
complete -o default -F _bkpr bkpr
`

const zshCompletion = `#compdef bkpr
# zsh completion for bkpr; source with: eval "$(bkpr completion zsh)"
_bkpr() {
    local -a args
    args=(${words[2,CURRENT-1]})
    args+=("${words[CURRENT]}")
    local out
    out="$(bkpr __complete "${args[@]}" 2>/dev/null)"
    local -a lines
    lines=("${(@f)out}")
    local directive="${lines[-1]}"
    local -a cands
    if [[ "$directive" == :* ]]; then
        cands=(${lines[1,-2]})
    else
        cands=(${lines})
        directive=":0"
    fi
    (( ${#cands} )) && compadd -- $cands
    if [[ "$directive" != *4* ]]; then
        _default
    fi
}
compdef _bkpr bkpr
`

const fishCompletion = `# fish completion for bkpr; source with: bkpr completion fish | source
function __bkpr_complete
    set -l tokens (commandline -opc)
    set -l current (commandline -ct)
    bkpr __complete $tokens[2..-1] $current 2>/dev/null | string match -v -r '^:'
end
complete -c bkpr -a '(__bkpr_complete)'
`
