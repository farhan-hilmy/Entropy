package config

// The experiment editor edits one experiment at a time as YAML text. The
// text replaces the experiment's lines in the file, so the user's comments
// and layout inside it are kept, and everything else in the file is left
// exactly as it was. Apply's safety net still checks the result.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// ExperimentSource is one experiment's lines from the config file, for the
// editor, and the hash of the file to send back with the edit.
type ExperimentSource struct {
	Hash string `json:"hash"`
	YAML string `json:"yaml"`
}

// ReadExperiment returns the experiment called name as it's written in the
// file at path, comments included, without the list's dash and indentation.
func ReadExperiment(path, name string) (ExperimentSource, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ExperimentSource{}, err
	}
	cfg, err := decode(data, path)
	if err != nil {
		return ExperimentSource{}, &InvalidError{err}
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return ExperimentSource{}, &InvalidError{err}
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return ExperimentSource{}, ErrUnsafe
	}
	m, err := item(root.Content[0], "experiments", "experiment", name)
	if err != nil {
		return ExperimentSource{}, err
	}
	ed := newEditor(data)
	if _, err := ed.dashColumn(m); err != nil {
		return ExperimentSource{}, err
	}
	keys := m.Column - 1
	var lines []string
	for n := m.Line; n <= lastLine(m); n++ {
		line := []rune(ed.lines[n-1])
		if n == m.Line {
			lines = append(lines, string(line[keys:]))
			continue
		}
		cut := 0
		for cut < keys && cut < len(line) && line[cut] == ' ' {
			cut++
		}
		lines = append(lines, string(line[cut:]))
	}
	text := strings.Join(lines, "\n") + "\n"

	// What the editor shows must mean the experiment in the file, or saving
	// it back would change more than the user did (e.g. a multi-line value
	// running past the experiment's last key).
	got, _, err := parseExperimentText(text)
	i := slices.IndexFunc(cfg.Experiments, func(x Experiment) bool { return x.Name == name })
	if err != nil || i < 0 || !reflect.DeepEqual(got, cfg.Experiments[i]) {
		return ExperimentSource{}, ErrUnsafe
	}
	return ExperimentSource{Hash: hashOf(data), YAML: text}, nil
}

// setExperiment replaces the experiment named t.Name with the one in t.YAML,
// or appends it when t.Name is empty.
func setExperiment(cfg *Config, doc *yaml.Node, ed *editor, t ExperimentText) error {
	exp, lines, err := parseExperimentText(t.YAML)
	if err != nil {
		return &InvalidError{err}
	}
	render := func(dash, keys int) []string { return renderItem(lines, dash, keys) }
	if t.Name == "" {
		cfg.Experiments = append(cfg.Experiments, exp)
		return ed.appendLines(doc, "experiments", render)
	}
	m, err := item(doc, "experiments", "experiment", t.Name)
	if err != nil {
		return err
	}
	dash, err := ed.dashColumn(m)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(cfg.Experiments, func(x Experiment) bool { return x.Name == t.Name })
	if i < 0 {
		return fmt.Errorf("%w: no experiment %q in the config file", ErrNotFound, t.Name)
	}
	cfg.Experiments[i] = exp
	ed.replace(m.Line, lastLine(m), render(dash, m.Column-1)...)
	return nil
}

// parseExperimentText reads one experiment from the editor's text. The text
// may be indented as a whole, or be a list item ("- name: …") as Copy as YAML
// gives it. lines is the text without that indentation, dash and trailing
// blank lines; line numbers in errors are the text's own.
func parseExperimentText(text string) (Experiment, []string, error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	content := func(l string) bool { t := strings.TrimSpace(l); return t != "" && !strings.HasPrefix(t, "#") }
	first := slices.IndexFunc(lines, content)
	if first < 0 {
		return Experiment{}, nil, errors.New("the experiment is empty")
	}
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimLeft(l, " "), "\t") {
			return Experiment{}, nil, fmt.Errorf("line %d: indent with spaces, not tabs", i+1)
		}
	}
	dedent := func(lines []string, n int) {
		for i, l := range lines {
			cut := 0
			for cut < n && cut < len(l) && l[cut] == ' ' {
				cut++
			}
			lines[i] = l[cut:]
		}
	}
	indent := func(l string) int { return len(l) - len(strings.TrimLeft(l, " ")) }
	minIndent := -1
	for _, l := range lines {
		if content(l) && (minIndent < 0 || indent(l) < minIndent) {
			minIndent = indent(l)
		}
	}
	dedent(lines, minIndent)
	if rest, ok := strings.CutPrefix(lines[first], "- "); ok {
		lines[first] = "  " + rest
		dedent(lines, 2)
	}
	src := strings.Join(lines, "\n") + "\n"

	var node yaml.Node
	dec := yaml.NewDecoder(strings.NewReader(src))
	if err := dec.Decode(&node); err != nil {
		return Experiment{}, nil, fmt.Errorf("experiment: %w", err)
	}
	if node.Kind != yaml.DocumentNode || len(node.Content) != 1 || node.Content[0].Kind != yaml.MappingNode || node.Content[0].Style&yaml.FlowStyle != 0 {
		return Experiment{}, nil, errors.New("experiment: write one experiment as keys and values (name: …, faults: …), one per line")
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return Experiment{}, nil, errors.New("experiment: write one experiment only (no --- or a second document)")
	}
	var exp Experiment
	strict := yaml.NewDecoder(bytes.NewReader([]byte(src)))
	strict.KnownFields(true)
	if err := strict.Decode(&exp); err != nil {
		return Experiment{}, nil, fmt.Errorf("experiment: %w", err)
	}
	return exp, lines, nil
}

// renderItem turns the editor's lines into a list entry: comments before the
// first key go above the dash, the first key goes on the dash's line, and the
// rest is indented to the keys' column.
func renderItem(lines []string, dash, keys int) []string {
	out := make([]string, 0, len(lines))
	started := false
	for _, l := range lines {
		switch t := strings.TrimSpace(l); {
		case t == "":
			out = append(out, "")
		case !started && strings.HasPrefix(t, "#"):
			out = append(out, strings.Repeat(" ", dash)+l)
		case !started:
			started = true
			out = append(out, strings.Repeat(" ", dash)+"-"+strings.Repeat(" ", keys-dash-1)+l)
		default:
			out = append(out, strings.Repeat(" ", keys)+l)
		}
	}
	return out
}

// dashColumn is the column (0-based) of the "- " that starts the list entry
// m, which must be on the line of m's first key.
func (ed *editor) dashColumn(m *yaml.Node) (int, error) {
	if m.Style&yaml.FlowStyle != 0 {
		return 0, ErrUnsafe
	}
	line := []rune(ed.lines[m.Line-1])
	if m.Column-1 > len(line) {
		return 0, ErrUnsafe
	}
	before := strings.TrimRight(string(line[:m.Column-1]), " ")
	if strings.TrimLeft(before, " ") != "-" {
		return 0, ErrUnsafe
	}
	return len(before) - 1, nil
}
