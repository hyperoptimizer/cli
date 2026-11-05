package config

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/natefinch/atomic"
	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

//go:embed format-policy.json
var formatPolicyJSON []byte

type formattingPolicy struct {
	Fields      map[string][]string `json:"fields"`
	NamedArrays map[string][]string `json:"namedArrays"`
}

// Comments are attached to semantic nodes, so dotted/inline/table syntax can all
// render identically, and a named block carries its comments when sorted.
type formatNode struct {
	value                any
	fields               map[string]*formatNode
	items                []*formatNode
	before, inline, tail []string
}

func tree(value any) *formatNode {
	n := &formatNode{value: value}
	switch v := value.(type) {
	case map[string]any:
		n.fields = make(map[string]*formatNode, len(v))
		for k, child := range v {
			n.fields[k] = tree(child)
		}
	case []any:
		n.items = make([]*formatNode, len(v))
		for i, child := range v {
			n.items[i] = tree(child)
		}
	}
	return n
}

func keys(n *unstable.Node) []string {
	var result []string
	for it := n.Key(); it.Next(); {
		result = append(result, string(it.Node().Data))
	}
	return result
}

func child(n *formatNode, path []string) *formatNode {
	for _, key := range path {
		n = n.fields[key]
	}
	return n
}

func nodeSpan(n *unstable.Node) (int, int) {
	start, end := math.MaxInt, 0
	var visit func(*unstable.Node)
	visit = func(v *unstable.Node) {
		if v.Raw.Length > 0 {
			a, b := int(v.Raw.Offset), int(v.Raw.Offset+v.Raw.Length)
			if a < start {
				start = a
			}
			if b > end {
				end = b
			}
		}
		for c := v.Child(); c != nil; c = c.Next() {
			visit(c)
		}
	}
	visit(n)
	return start, end
}

func sameLine(source []byte, a, b int) bool {
	return a <= b && !bytes.Contains(source[a:b], []byte("\n"))
}

func comments(n *unstable.Node) []string {
	result := []string{strings.TrimSpace(string(n.Data))}
	for c := n.Child(); c != nil; c = c.Next() {
		result = append(result, comments(c)...)
	}
	return result
}

func annotateValue(dst *formatNode, src *unstable.Node, source []byte) {
	switch src.Kind {
	case unstable.InlineTable:
		for kv := src.Child(); kv != nil; kv = kv.Next() {
			if kv.Kind == unstable.KeyValue {
				annotateValue(child(dst, keys(kv)), kv.Value(), source)
			}
		}
	case unstable.Array:
		var pending []string
		index, lastEnd := 0, -1
		var previous *formatNode
		for c := src.Child(); c != nil; c = c.Next() {
			if c.Kind == unstable.Comment {
				start, _ := nodeSpan(c)
				if previous != nil && sameLine(source, lastEnd, start) {
					previous.inline = append(previous.inline, strings.TrimSpace(string(c.Data)))
					for nested := c.Child(); nested != nil; nested = nested.Next() {
						pending = append(pending, comments(nested)...)
					}
				} else {
					pending = append(pending, comments(c)...)
				}
				continue
			}
			current := dst.items[index]
			index++
			current.before = append(current.before, pending...)
			pending = nil
			annotateValue(current, c, source)
			_, lastEnd = nodeSpan(c)
			previous = current
		}
		dst.tail = append(dst.tail, pending...)
	}
}

func annotate(root *formatNode, source []byte) error {
	var parser unstable.Parser
	parser.KeepComments = true
	parser.Reset(source)
	context := root
	indices := map[*formatNode]int{}
	var pending []string
	first := true
	lastCommentEnd := 0
	for parser.NextExpression() {
		expression := parser.Expression()
		if expression.Kind == unstable.Comment {
			pending = append(pending, comments(expression)...)
			_, lastCommentEnd = nodeSpan(expression)
			continue
		}
		start, _ := nodeSpan(expression)
		if first && len(pending) > 0 && bytes.Count(source[lastCommentEnd:start], []byte("\n")) > 1 {
			root.before = append(root.before, pending...)
			pending = nil
		}
		first = false
		var dst *formatNode
		switch expression.Kind {
		case unstable.Table, unstable.ArrayTable:
			dst = root
			parts := keys(expression)
			for i, key := range parts {
				dst = dst.fields[key]
				if dst.items != nil {
					index, exists := indices[dst]
					if expression.Kind == unstable.ArrayTable && i == len(parts)-1 {
						if exists {
							index++
						}
						indices[dst] = index
					}
					dst = dst.items[index]
				}
			}
			context = dst
		case unstable.KeyValue:
			dst = child(context, keys(expression))
			annotateValue(dst, expression.Value(), source)
		default:
			return fmt.Errorf("unsupported TOML expression: %s", expression.Kind)
		}
		dst.before = append(dst.before, pending...)
		pending = nil
		for next := expression.Next(); next != nil; next = next.Next() {
			if next.Kind == unstable.Comment {
				dst.inline = append(dst.inline, comments(next)...)
			}
		}
	}
	root.tail = append(root.tail, pending...)
	return parser.Error()
}

func quoted(value string) string {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	// DEL is legal in JSON, but TOML basic strings require it to be escaped.
	return strings.ReplaceAll(strings.TrimSuffix(buffer.String(), "\n"), "\u007f", `\u007f`)
}

func formatKey(value string) string {
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return quoted(value)
		}
	}
	if value == "" {
		return quoted(value)
	}
	return value
}
func number(value float64) string {
	if math.IsNaN(value) {
		return "nan"
	}
	if math.IsInf(value, 1) {
		return "inf"
	}
	if math.IsInf(value, -1) {
		return "-inf"
	}
	absolute := math.Abs(value)
	if value == 0 && math.Signbit(value) {
		return "-0.0"
	}
	if absolute == 0 || absolute >= 1e-6 && absolute < 1e21 {
		s := strconv.FormatFloat(value, 'f', -1, 64)
		// A decimal integer outside int64 would no longer be valid TOML.
		if absolute >= math.MaxInt64 && !strings.Contains(s, ".") {
			s += ".0"
		}
		return s
	}
	s := strconv.FormatFloat(value, 'e', -1, 64)
	parts := strings.Split(s, "e")
	exponent, _ := strconv.Atoi(parts[1])
	return parts[0] + "e" + fmt.Sprintf("%+d", exponent)
}
func scalar(value any) (string, error) {
	switch v := value.(type) {
	case string:
		return quoted(v), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case float64:
		return number(v), nil
	case bool:
		return strconv.FormatBool(v), nil
	case time.Time:
		return v.Format(time.RFC3339Nano), nil
	default:
		// Preserve local date/time values using the existing TOML encoder.
		b, err := toml.Marshal(map[string]any{"value": value})
		if err != nil {
			return "", err
		}
		return strings.TrimPrefix(strings.TrimSpace(string(b)), "value = "), nil
	}
}

func tableArray(n *formatNode) bool {
	if len(n.items) == 0 {
		return false
	}
	for _, item := range n.items {
		if item.fields == nil {
			return false
		}
	}
	return true
}
func suffix(comments []string) string {
	if len(comments) == 0 {
		return ""
	}
	return " " + strings.Join(comments, "\n")
}
func orderedKeys(n *formatNode, path string, policy formattingPolicy) []string {
	fields := policy.Fields[path]
	rank := func(key string) int {
		for i, name := range fields {
			if key == name {
				return i
			}
		}
		return len(fields)
	}
	result := make([]string, 0, len(n.fields))
	for k := range n.fields {
		result = append(result, k)
	}
	sort.SliceStable(result, func(i, j int) bool {
		a, b := rank(result[i]), rank(result[j])
		if a != b {
			return a < b
		}
		return result[i] < result[j]
	})
	return result
}
func hasComments(n *formatNode) bool {
	if len(n.before)+len(n.inline)+len(n.tail) > 0 {
		return true
	}
	for _, item := range n.items {
		if hasComments(item) {
			return true
		}
	}
	return false
}
func formatValue(n *formatNode, indent string) (string, error) {
	if n.fields != nil {
		var entries []string
		for _, key := range orderedKeys(n, "", formattingPolicy{}) {
			value, err := formatValue(n.fields[key], indent)
			if err != nil {
				return "", err
			}
			entries = append(entries, formatKey(key)+" = "+value)
		}
		return "{" + strings.Join(entries, ", ") + "}", nil
	}
	if n.items == nil {
		return scalar(n.value)
	}
	multiline := len(n.tail) > 0
	for _, item := range n.items {
		multiline = multiline || hasComments(item)
	}
	var items []string
	for _, item := range n.items {
		value, err := formatValue(item, indent+"  ")
		if err != nil {
			return "", err
		}
		if multiline {
			for _, c := range item.before {
				items = append(items, indent+"  "+c)
			}
			items = append(items, indent+"  "+value+","+suffix(item.inline))
		} else {
			items = append(items, value)
		}
	}
	if !multiline {
		return "[" + strings.Join(items, ", ") + "]", nil
	}
	for _, c := range n.tail {
		items = append(items, indent+"  "+c)
	}
	return "[\n" + strings.Join(items, "\n") + "\n" + indent + "]", nil
}

func render(root *formatNode) ([]byte, error) {
	var policy formattingPolicy
	if err := json.Unmarshal(formatPolicyJSON, &policy); err != nil {
		return nil, err
	}
	var lines []string
	var emit func(*formatNode, []string, bool) error
	emit = func(n *formatNode, path []string, array bool) error {
		if len(path) > 0 {
			if len(lines) > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, n.before...)
			encoded := make([]string, len(path))
			for i, k := range path {
				encoded[i] = formatKey(k)
			}
			name := strings.Join(encoded, ".")
			header := "[" + name + "]"
			if array {
				header = "[[" + name + "]]"
			}
			lines = append(lines, header+suffix(n.inline))
		} else {
			lines = append(lines, n.before...)
			if len(n.before) > 0 {
				lines = append(lines, "")
			}
		}
		names := orderedKeys(n, strings.Join(path, "."), policy)
		for _, key := range names {
			v := n.fields[key]
			if v.fields != nil || tableArray(v) {
				continue
			}
			lines = append(lines, v.before...)
			value, err := formatValue(v, "")
			if err != nil {
				return err
			}
			lines = append(lines, formatKey(key)+" = "+value+suffix(v.inline))
		}
		for _, key := range names {
			v := n.fields[key]
			next := append(append([]string{}, path...), key)
			if v.fields != nil {
				if err := emit(v, next, false); err != nil {
					return err
				}
			}
			if tableArray(v) {
				items := append([]*formatNode{}, v.items...)
				if fields := policy.NamedArrays[strings.Join(next, ".")]; len(fields) > 0 {
					sort.SliceStable(items, func(i, j int) bool {
						for _, field := range fields {
							a, b := "", ""
							if item := items[i].fields[field]; item != nil {
								a = fmt.Sprint(item.value)
							}
							if item := items[j].fields[field]; item != nil {
								b = fmt.Sprint(item.value)
							}
							if a != b {
								return a < b
							}
						}
						return false
					})
				}
				// Comments on an inline array declaration describe the collection itself.
				if len(v.before)+len(v.inline) > 0 {
					first := *items[0]
					first.before = append(append(append([]string{}, v.before...), v.inline...), first.before...)
					items[0] = &first
				}
				for _, item := range items {
					if err := emit(item, next, true); err != nil {
						return err
					}
				}
				lines = append(lines, v.tail...)
			}
		}
		lines = append(lines, n.tail...)
		return nil
	}
	if err := emit(root, nil, false); err != nil {
		return nil, err
	}
	return []byte(strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"), nil
}

// Format parses without schema defaults. Malformed/duplicate TOML is rejected
// before any write, including syntax that the low-level AST alone accepts.
func Format(source []byte) ([]byte, error) {
	if len(source) > 100000 {
		return nil, fmt.Errorf("TOML document exceeds 100 KB")
	}
	source = bytes.ReplaceAll(source, []byte("\r\n"), []byte("\n"))
	var value map[string]any
	if err := toml.Unmarshal(source, &value); err != nil {
		return nil, fmt.Errorf("invalid TOML: %w", err)
	}
	root := tree(value)
	if err := annotate(root, source); err != nil {
		return nil, fmt.Errorf("invalid TOML: %w", err)
	}
	formatted, err := render(root)
	if err != nil {
		return nil, err
	}
	var verified map[string]any
	if err := toml.Unmarshal(formatted, &verified); err != nil {
		return nil, fmt.Errorf("cannot format TOML: %w", err)
	}
	return formatted, nil
}

// FormatFile replaces a regular file atomically, retaining its permission bits.
// Symlinks are rejected rather than inadvertently replacing the link itself.
func FormatFile(path string, check bool) (bool, error) {
	stat, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	if !stat.Mode().IsRegular() {
		return false, fmt.Errorf("configuration must be a regular file")
	}
	if stat.Size() > 100000 {
		return false, fmt.Errorf("TOML document exceeds 100 KB")
	}
	source, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	formatted, err := Format(source)
	if err != nil {
		return false, err
	}
	changed := !bytes.Equal(source, formatted)
	if check || !changed {
		return changed, nil
	}
	return changed, atomic.WriteFile(path, bytes.NewReader(formatted))
}
