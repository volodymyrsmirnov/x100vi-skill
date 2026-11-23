package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/errs"
)

// A small argparse: subcommands, options anywhere among positionals, options that take several
// values (-r a b c), choices, and generated help. Usage errors exit 2, like argparse.

type optKind int

const (
	optBool optKind = iota
	optString
	optFloat
	optList
)

type option struct {
	long, short string
	kind        optKind
	help        string
	metavar     string
	choices     []string
	hidden      bool
	def         any
}

type positional struct {
	name    string
	help    string
	many    bool // one or more
	opt     bool // zero or one
	def     string
	metavar string
}

type command struct {
	name    string
	help    string
	options []option
	args    []positional
}

// Args is a parsed command line.
type Args struct {
	Command string
	values  map[string]any
	pos     map[string][]string
}

func (a *Args) Bool(name string) bool { v, _ := a.values[name].(bool); return v }
func (a *Args) Str(name string) string {
	v, _ := a.values[name].(string)
	return v
}
func (a *Args) Float(name string) float64 { v, _ := a.values[name].(float64); return v }
func (a *Args) List(name string) []string { v, _ := a.values[name].([]string); return v }
func (a *Args) Has(name string) bool {
	v, ok := a.values[name]
	return ok && v != nil
}
func (a *Args) Pos(name string) string {
	if v := a.pos[name]; len(v) > 0 {
		return v[0]
	}
	return ""
}
func (a *Args) PosList(name string) []string { return a.pos[name] }

func key(o option) string { return strings.TrimPrefix(o.long, "--") }

func usageErr(cmd string, format string, args ...any) error {
	prog := "x100vi"
	if cmd != "" {
		prog += " " + cmd
	}
	return errs.Recipe(fmt.Sprintf(format, args...), fmt.Sprintf("Run `%s --help` for usage.", prog))
}

func isOption(tok string) bool {
	if !strings.HasPrefix(tok, "-") || tok == "-" {
		return false
	}
	if _, err := strconv.ParseFloat(tok, 64); err == nil {
		return false // a negative number is a value
	}
	return true
}

func (c *command) find(tok string) (*option, string, bool) {
	name, value, hasValue := strings.Cut(tok, "=")
	for i := range c.options {
		o := &c.options[i]
		if name == o.long || (o.short != "" && name == o.short) {
			return o, value, hasValue
		}
	}
	return nil, "", false
}

func (c *command) parse(tokens []string) (*Args, error) {
	a := &Args{Command: c.name, values: map[string]any{}, pos: map[string][]string{}}
	for _, o := range c.options {
		a.values[key(o)] = o.def
	}
	var loose []string
	onlyPositional := false
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if onlyPositional || !isOption(tok) {
			loose = append(loose, tok)
			continue
		}
		if tok == "--" {
			onlyPositional = true
			continue
		}
		o, inline, hasInline := c.find(tok)
		if o == nil {
			return nil, usageErr(c.name, "unrecognized option: %s", tok)
		}
		switch o.kind {
		case optBool:
			if hasInline {
				return nil, usageErr(c.name, "%s takes no value", o.long)
			}
			a.values[key(*o)] = true
		case optString, optFloat:
			v := inline
			if !hasInline {
				if i+1 >= len(tokens) {
					return nil, usageErr(c.name, "%s needs a value", o.long)
				}
				i++
				v = tokens[i]
			}
			if len(o.choices) > 0 && !contains(o.choices, v) {
				return nil, usageErr(c.name, "%s: invalid choice: '%s' (choose from %s)", o.long, v, strings.Join(o.choices, ", "))
			}
			if o.kind == optFloat {
				f, err := strconv.ParseFloat(v, 64)
				if err != nil {
					return nil, usageErr(c.name, "%s: invalid number: '%s'", o.long, v)
				}
				a.values[key(*o)] = f
			} else {
				a.values[key(*o)] = v
			}
		case optList:
			list, _ := a.values[key(*o)].([]string)
			if hasInline {
				list = append(list, inline)
			}
			for i+1 < len(tokens) && !isOption(tokens[i+1]) {
				i++
				list = append(list, tokens[i])
			}
			if len(list) == 0 {
				return nil, usageErr(c.name, "%s needs at least one value", o.long)
			}
			a.values[key(*o)] = list
		}
	}
	// Assign positionals in order; a "many" positional takes everything that is left except what
	// later positionals need.
	for idx, p := range c.args {
		need := 0
		for _, later := range c.args[idx+1:] {
			if !later.opt {
				need++
			}
		}
		switch {
		case p.many:
			take := len(loose) - need
			if take < 1 {
				return nil, usageErr(c.name, "the following arguments are required: %s", p.display())
			}
			a.pos[p.name] = loose[:take]
			loose = loose[take:]
		case p.opt:
			if len(loose) > need {
				a.pos[p.name] = loose[:1]
				loose = loose[1:]
			} else if p.def != "" {
				a.pos[p.name] = []string{p.def}
			}
		default:
			if len(loose) == 0 {
				return nil, usageErr(c.name, "the following arguments are required: %s", p.display())
			}
			a.pos[p.name] = loose[:1]
			loose = loose[1:]
		}
	}
	if len(loose) > 0 {
		return nil, usageErr(c.name, "unrecognized arguments: %s", strings.Join(loose, " "))
	}
	return a, nil
}

func (p positional) display() string {
	if p.metavar != "" {
		return p.metavar
	}
	return p.name
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (o option) usage() string {
	name := o.long
	if o.short != "" {
		name = o.short + ", " + o.long
	}
	meta := o.metavar
	if meta == "" {
		meta = strings.ToUpper(strings.ReplaceAll(key(o), "-", "_"))
	}
	if len(o.choices) > 0 {
		meta = "{" + strings.Join(o.choices, ",") + "}"
	}
	switch o.kind {
	case optString, optFloat:
		name += " " + meta
	case optList:
		name += " " + meta + " [" + meta + " ...]"
	}
	return name
}

func (c *command) helpText() string {
	var b strings.Builder
	line := "usage: x100vi " + c.name + " [options]"
	for _, p := range c.args {
		switch {
		case p.many:
			line += " " + p.display() + " [" + p.display() + " ...]"
		case p.opt:
			line += " [" + p.display() + "]"
		default:
			line += " " + p.display()
		}
	}
	b.WriteString(line + "\n\n" + c.help + "\n")
	if len(c.args) > 0 {
		b.WriteString("\npositional arguments:\n")
		for _, p := range c.args {
			fmt.Fprintf(&b, "  %-24s %s\n", p.display(), p.help)
		}
	}
	b.WriteString("\noptions:\n")
	fmt.Fprintf(&b, "  %-24s %s\n", "-h, --help", "show this help and exit")
	fmt.Fprintf(&b, "  %-24s %s\n", "--json", "print one JSON object instead of text")
	for _, o := range c.options {
		if o.hidden || o.long == "--json" {
			continue
		}
		u := o.usage()
		if len(u) > 24 {
			fmt.Fprintf(&b, "  %s\n  %-24s %s\n", u, "", o.help)
		} else {
			fmt.Fprintf(&b, "  %-24s %s\n", u, o.help)
		}
	}
	return b.String()
}
