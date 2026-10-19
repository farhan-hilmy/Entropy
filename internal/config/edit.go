package config

// Editing changes a few fields of the config file in place, for the UI. Only
// fields that can't make Entropy execute anything are editable: commands
// (run, cwd) are not, so the UI can't be used to add code that Entropy runs.
//
// The file is edited as text, not re-encoded, so comments, blank lines and
// alignment survive. The result is parsed back and must differ from the
// original only in the edited fields, otherwise nothing is written.

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var (
	ErrConflict = errors.New("the config file changed since it was read; reload it and try again")
	ErrNotFound = errors.New("not found")
	errUnsafe   = errors.New("this part of the config file can't be edited safely from the UI; edit the file instead")
)

// InvalidError means the file, or the file after the edit, is not a valid config.
type InvalidError struct{ Err error }

func (e *InvalidError) Error() string { return e.Err.Error() }
func (e *InvalidError) Unwrap() error { return e.Err }

// Snapshot is the config file as written, without defaults, and a hash of
// its content to pass back to Apply.
type Snapshot struct {
	Hash   string  `json:"hash"`
	Config *Config `json:"config"`
}

func Read(path string) (Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, err
	}
	cfg, err := decode(data, path)
	if err != nil {
		return Snapshot{}, &InvalidError{err}
	}
	return Snapshot{Hash: hashOf(data), Config: cfg}, nil
}

type EnvironmentFields struct {
	Name         string `json:"name"`
	ToxiproxyAPI string `json:"toxiproxyApi"`
}

type DependencyFields struct {
	Name     string `json:"name"` // which dependency; names can't be edited
	Address  string `json:"address"`
	Upstream string `json:"upstream"`
	Listen   string `json:"listen"`
	Redirect bool   `json:"redirect"`
}

type ServiceFields struct {
	Name           string   `json:"name"` // which service; names can't be edited
	BaseURL        string   `json:"baseUrl"`
	HealthCheck    string   `json:"healthCheck"`
	HealthCommand  string   `json:"healthCommand"`
	HealthEverySec int      `json:"healthEverySec"`
	Uses           []string `json:"uses"`
}

// Edit replaces the editable fields of one part of the config: set exactly
// one of its fields. Empty optional fields are removed from the file, so
// their defaults apply.
type Edit struct {
	Environment *EnvironmentFields `json:"environment,omitempty"`
	Dependency  *DependencyFields  `json:"dependency,omitempty"`
	Service     *ServiceFields     `json:"service,omitempty"`
}

// Apply writes e to the config file at path, provided its content still
// hashes to hash, and returns the new hash. An edit that would leave the
// file invalid changes nothing and returns an *InvalidError.
func Apply(path, hash string, e Edit) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if hashOf(data) != hash {
		return "", ErrConflict
	}
	want, err := decode(data, path)
	if err != nil {
		return "", &InvalidError{err}
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return "", &InvalidError{err}
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return "", errUnsafe
	}
	ed := newEditor(data)
	if err := e.apply(want, root.Content[0], ed); err != nil {
		return "", err
	}
	out := ed.result()

	if _, err := parse(out, path); err != nil {
		return "", &InvalidError{err}
	}
	// The safety net: the edited text must mean exactly the intended config.
	if got, err := decode(out, path); err != nil || !reflect.DeepEqual(got, want) {
		return "", errUnsafe
	}
	if err := writeFile(path, out); err != nil {
		return "", err
	}
	return hashOf(out), nil
}

// apply sets the fields both on cfg, the expected result, and in the file's text.
func (e Edit) apply(cfg *Config, doc *yaml.Node, ed *editor) error {
	set := 0
	for _, part := range []bool{e.Environment != nil, e.Dependency != nil, e.Service != nil} {
		if part {
			set++
		}
	}
	if set != 1 {
		return errors.New("an edit sets exactly one of environment, dependency or service")
	}
	switch {
	case e.Environment != nil:
		f := e.Environment
		cfg.Environment.Name = f.Name
		cfg.Toxiproxy.API = f.ToxiproxyAPI
		return errors.Join(
			ed.setIn(doc, "environment", "name", optional(f.Name)),
			ed.setIn(doc, "toxiproxy", "api", optional(f.ToxiproxyAPI)),
		)

	case e.Dependency != nil:
		f := e.Dependency
		m, err := item(doc, "dependencies", "dependency", f.Name)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(cfg.Dependencies, func(d Dependency) bool { return d.Name == f.Name })
		d := &cfg.Dependencies[i]
		d.Address, d.Upstream, d.Listen, d.Redirect = f.Address, f.Upstream, f.Listen, f.Redirect
		return errors.Join(
			ed.set(m, "address", required(f.Address)),
			ed.set(m, "upstream", required(f.Upstream)),
			ed.set(m, "listen", optional(f.Listen)),
			ed.set(m, "redirect", flag(f.Redirect)),
		)

	default:
		f := e.Service
		m, err := item(doc, "services", "service", f.Name)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(cfg.Services, func(s Service) bool { return s.Name == f.Name })
		s := &cfg.Services[i]
		s.BaseURL, s.HealthCheck, s.HealthCommand, s.HealthEverySec = f.BaseURL, f.HealthCheck, f.HealthCommand, f.HealthEverySec
		s.Uses = nil
		if len(f.Uses) > 0 {
			s.Uses = slices.Clone(f.Uses)
		}
		every := ""
		if f.HealthEverySec != 0 {
			every = strconv.Itoa(f.HealthEverySec)
		}
		return errors.Join(
			ed.set(m, "baseUrl", required(f.BaseURL)),
			ed.set(m, "healthCheck", optional(f.HealthCheck)),
			ed.set(m, "healthCommand", optional(f.HealthCommand)),
			ed.set(m, "healthEverySec", every),
			ed.set(m, "uses", list(f.Uses)),
		)
	}
}

// Values as YAML text. "" means: remove the key.

func required(s string) string {
	b, _ := yaml.Marshal(s)
	return strings.TrimSuffix(string(b), "\n")
}

func optional(s string) string {
	if s == "" {
		return ""
	}
	return required(s)
}

func flag(b bool) string {
	if b {
		return "true"
	}
	return ""
}

func list(items []string) string {
	if len(items) == 0 {
		return ""
	}
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = required(s)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

func lookup(m *yaml.Node, key string) (k, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i], m.Content[i+1]
		}
	}
	return nil, nil
}

// item finds the entry called name in the doc's list section.
func item(doc *yaml.Node, section, what, name string) (*yaml.Node, error) {
	if _, seq := lookup(doc, section); seq != nil && seq.Kind == yaml.SequenceNode {
		for _, it := range seq.Content {
			if it.Kind != yaml.MappingNode {
				continue
			}
			if _, n := lookup(it, "name"); n != nil && n.Value == name {
				return it, nil
			}
		}
	}
	return nil, fmt.Errorf("%w: no %s %q in the config file", ErrNotFound, what, name)
}

// editor collects line edits against the original text and applies them all at once.
type editor struct {
	lines []string // without line endings
	eol   string
	ops   []lineOp
}

// lineOp replaces lines from..to (1-based, inclusive) with text.
// from == to+1 inserts after line to.
type lineOp struct {
	from, to int
	text     []string
	seq      int // order of the call, so insertions at one place keep it
}

func newEditor(data []byte) *editor {
	s := string(data)
	eol := "\n"
	if strings.Contains(s, "\r\n") {
		eol = "\r\n"
	}
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	return &editor{lines: lines, eol: eol}
}

func (ed *editor) replace(from, to int, text ...string) {
	ed.ops = append(ed.ops, lineOp{from, to, text, len(ed.ops)})
}

func (ed *editor) result() []byte {
	// Bottom-up, so earlier line numbers stay valid. On the same line, a
	// replacement goes before an insertion after it, and insertions at the
	// same place go last-call-first so they end up in call order.
	slices.SortFunc(ed.ops, func(a, b lineOp) int {
		return cmp.Or(cmp.Compare(b.from, a.from), cmp.Compare(b.to, a.to), cmp.Compare(b.seq, a.seq))
	})
	lines := slices.Clone(ed.lines)
	for _, op := range ed.ops {
		lines = slices.Replace(lines, op.from-1, op.to, op.text...)
	}
	return []byte(strings.Join(lines, ed.eol))
}

// setIn sets key in a top-level mapping section, adding the section if needed.
func (ed *editor) setIn(doc *yaml.Node, section, key, text string) error {
	k, v := lookup(doc, section)
	switch {
	case v != nil && v.Kind == yaml.MappingNode:
		return ed.set(v, key, text)
	case text == "":
		return nil
	case v == nil:
		last := len(ed.lines)
		if ed.lines[last-1] == "" { // the file ends with a line break
			last--
		}
		ed.replace(last+1, last, "", section+":", "  "+key+": "+text)
		return nil
	case v.Kind == yaml.ScalarNode && v.Tag == "!!null" && v.Line == k.Line:
		indent := strings.Repeat(" ", k.Column-1)
		ed.replace(k.Line, k.Line, indent+section+":", indent+"  "+key+": "+text)
		return nil
	}
	return errUnsafe
}

// set sets key in the block mapping m to text, or removes the key when text is "".
func (ed *editor) set(m *yaml.Node, key, text string) error {
	if strings.ContainsAny(text, "\r\n") {
		return fmt.Errorf("%s can't contain a line break", key)
	}
	if m.Style&yaml.FlowStyle != 0 || len(m.Content) == 0 {
		return errUnsafe
	}
	indent := strings.Repeat(" ", m.Content[0].Column-1)
	k, v := lookup(m, key)

	if k == nil {
		if text == "" {
			return nil
		}
		// Add it after the last key that holds a one-line value, so it lands
		// with the other simple fields rather than after a nested list.
		after := m.Content[0].Line
		for i := 0; i+1 < len(m.Content); i += 2 {
			if val := m.Content[i+1]; val.Kind == yaml.ScalarNode || val.Style&yaml.FlowStyle != 0 {
				after = lastLine(val)
			}
		}
		ed.replace(after+1, after, indent+key+": "+text)
		return nil
	}

	switch {
	case text == "":
		ed.replace(k.Line, lastLine(v))
	case v.Line == k.Line && lastLine(v) == v.Line && !(v.Tag == "!!null" && v.Value == ""):
		// Swap just the value, keeping the spacing and any comment after it.
		line := []rune(ed.lines[v.Line-1])
		start := v.Column - 1
		if end, ok := valueEnd(line, start); ok {
			ed.replace(v.Line, v.Line, string(line[:start])+text+string(line[end:]))
			return nil
		}
		fallthrough
	default:
		ed.replace(k.Line, lastLine(v), indent+key+": "+text)
	}
	return nil
}

func lastLine(n *yaml.Node) int {
	last := n.Line
	for _, c := range n.Content {
		last = max(last, lastLine(c))
	}
	return last
}

// valueEnd finds where the one-line value starting at line[start] ends.
// ok is false when it doesn't end on this line.
func valueEnd(line []rune, start int) (end int, ok bool) {
	if start >= len(line) {
		return 0, false
	}
	switch line[start] {
	case '\'':
		for i := start + 1; i < len(line); i++ {
			if line[i] == '\'' {
				if i+1 < len(line) && line[i+1] == '\'' { // '' is an escaped quote
					i++
					continue
				}
				return i + 1, true
			}
		}
		return 0, false
	case '"':
		for i := start + 1; i < len(line); i++ {
			switch line[i] {
			case '\\':
				i++
			case '"':
				return i + 1, true
			}
		}
		return 0, false
	case '[', '{':
		depth := 0
		var quote rune
		for i := start; i < len(line); i++ {
			switch c := line[i]; {
			case quote != 0:
				if c == quote {
					quote = 0
				}
			case c == '\'' || c == '"':
				quote = c
			case c == '[' || c == '{':
				depth++
			case c == ']' || c == '}':
				if depth--; depth == 0 {
					return i + 1, true
				}
			}
		}
		return 0, false
	case '|', '>':
		return 0, false
	}
	// A plain value runs to a " #" comment or the end of the line.
	end = len(line)
	for i := start + 1; i < len(line); i++ {
		if line[i] == '#' && (line[i-1] == ' ' || line[i-1] == '\t') {
			end = i
			break
		}
	}
	for end > start && (line[end-1] == ' ' || line[end-1] == '\t') {
		end--
	}
	return end, true
}

func hashOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// writeFile replaces path's content atomically, so a crash can't leave half a config.
func writeFile(path string, data []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".entropy-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // fails harmlessly once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), info.Mode()); err != nil {
		return err
	}
	// On Windows, replacing a file fails while another program briefly has it
	// open, e.g. an antivirus scan or the editor reloading it. Retry a while.
	for range 20 {
		if err = os.Rename(tmp.Name(), path); err == nil || !errors.Is(err, fs.ErrPermission) {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
	return err
}
