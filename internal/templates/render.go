package templates

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// placeholder matches a task prompt's parameter slots, e.g. {{ecosystem}}.
var placeholder = regexp.MustCompile(`\{\{\s*([a-zA-Z][a-zA-Z0-9_-]*)\s*\}\}`)

// namePattern is what a template name may be: an identifier a CLI
// argument can carry without quoting.
var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

func validName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("%w: name %q must be letters, digits, dot, dash, underscore", ErrInvalidDefinition, name)
	}
	return nil
}

// Render substitutes a template's parameters into its task prompt.
// Supplied values win over defaults; a placeholder with neither is an
// error, and so is a supplied value the prompt never uses.
func Render(task string, defaults, supplied map[string]string) (string, error) {
	used := params(task)
	for name := range supplied {
		if _, ok := used[name]; !ok {
			return "", fmt.Errorf("%w %q", ErrUnknownParam, name)
		}
	}
	var missing []string
	out := placeholder.ReplaceAllStringFunc(task, func(match string) string {
		name := placeholder.FindStringSubmatch(match)[1]
		if v, ok := supplied[name]; ok {
			return v
		}
		if v, ok := defaults[name]; ok {
			return v
		}
		missing = append(missing, name)
		return match
	})
	if len(missing) > 0 {
		sort.Strings(missing)
		return "", fmt.Errorf("%w: %s", ErrMissingParam, strings.Join(missing, ", "))
	}
	return out, nil
}

// checkParams rejects defaults for parameters the prompt does not use, so
// a typo in a template definition surfaces at save time rather than as a
// silently unsubstituted prompt at 3am.
func checkParams(task string, defaults map[string]string) error {
	used := params(task)
	for name := range defaults {
		if _, ok := used[name]; !ok {
			return fmt.Errorf("%w %q: the task prompt has no {{%s}}", ErrUnknownParam, name, name)
		}
	}
	return nil
}

func params(task string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, m := range placeholder.FindAllStringSubmatch(task, -1) {
		out[m[1]] = struct{}{}
	}
	return out
}
