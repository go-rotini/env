package env

import (
	"encoding"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

// fieldDescriptor is one resolved leaf field after walking a struct.
// Key includes any envPrefix accumulation but not the loader-level
// [WithPrefix], which is applied at lookup so descriptors can be cached
// per [reflect.Type] independently of loader configuration.
type fieldDescriptor struct {
	// Index is the [reflect.Value.FieldByIndex] path.
	Index []int

	// GoName is the Go field name.
	GoName string

	// GoPath is the dotted Go path used in diagnostics ("DB.URL").
	GoPath string

	// Key is the resolved env-var name (without [WithPrefix]).
	Key string

	// Description is the envDesc tag text.
	Description string

	Required     bool
	NotEmpty     bool
	HasDefault   bool
	DefaultValue string
	Separator    string
	KVSeparator  string
	Expand       bool
	FromFile     bool
	Unset        bool
	Secret       bool
	Immutable    bool
	Layout       string
	Base64       bool
	Hex          bool
}

// structInfo is the cached field-collection result for a struct type.
// Err is non-nil for invalid tags or shapes; the loader returns it
// without re-walking.
type structInfo struct {
	Fields []fieldDescriptor
	Err    error
}

var structInfoCache sync.Map

// getStructInfo returns the cached structInfo for t, walking on the
// first request. Concurrent callers race; the loser's walk is
// discarded.
func getStructInfo(t reflect.Type) *structInfo {
	if cached, ok := structInfoCache.Load(t); ok {
		info, _ := cached.(*structInfo)
		return info
	}
	info := collectStructInfo(t)
	actual, _ := structInfoCache.LoadOrStore(t, info)
	stored, _ := actual.(*structInfo)
	return stored
}

func collectStructInfo(t reflect.Type) *structInfo {
	if t.Kind() != reflect.Struct {
		return &structInfo{
			Err: fmt.Errorf("got %s: %w", t, ErrNotStruct),
		}
	}
	w := walker{
		seen:           make(map[string]string),
		tagName:        "env",
		fieldNameXform: toScreamingSnake,
	}
	w.walk(t, nil, "", "")
	return &structInfo{
		Fields: w.fields,
		Err:    errors.Join(w.errs...),
	}
}

// walker carries the cross-recursion state for collectStructInfo.
// Loaders configured with [WithTagName] or [WithFieldNameTransform]
// supply custom tagName / fieldNameXform values and bypass the cache.
type walker struct {
	fields         []fieldDescriptor
	seen           map[string]string
	errs           []error
	tagName        string
	fieldNameXform func(string) string
}

// walk recursively walks t, populating w.fields and w.errs.
// parentIndex is the FieldByIndex prefix; parentPrefix is the
// accumulated envPrefix; parentGoPath is the dotted Go path to t.
func (w *walker) walk(t reflect.Type, parentIndex []int, parentPrefix, parentGoPath string) {
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() && !f.Anonymous {
			continue
		}

		envTag, hasEnvTag := f.Tag.Lookup(w.tagName)
		if hasEnvTag && envTag == "-" {
			continue
		}

		index := make([]int, 0, len(parentIndex)+1)
		index = append(index, parentIndex...)
		index = append(index, i)

		goPath := f.Name
		if parentGoPath != "" {
			goPath = parentGoPath + "." + f.Name
		}

		envPrefix := f.Tag.Get("envPrefix")
		envDesc := f.Tag.Get("envDesc")

		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}

		// Anonymous embed: recurse without prepending the embed's name
		// (Go promotes inner fields).
		if f.Anonymous && ft.Kind() == reflect.Struct && !isLeafStruct(ft) {
			w.walk(ft, index, parentPrefix+envPrefix, parentGoPath)
			continue
		}

		// Named struct field with no env tag: recurse. An explicit env
		// tag opts into treating the whole struct as a single keyed
		// leaf for the decoder to handle.
		if !hasEnvTag && ft.Kind() == reflect.Struct && !isLeafStruct(ft) && f.IsExported() {
			w.walk(ft, index, parentPrefix+envPrefix, goPath)
			continue
		}

		if !f.IsExported() {
			continue
		}

		tag, err := parseFieldTag(envTag)
		if err != nil {
			w.errs = append(w.errs, fmt.Errorf("field %s: %w", goPath, err))
			continue
		}

		var key string
		if tag.Name != "" {
			key = parentPrefix + tag.Name
		} else {
			key = parentPrefix + w.fieldNameXform(f.Name)
		}

		if existing, dup := w.seen[key]; dup {
			w.errs = append(w.errs, fmt.Errorf(
				"key %q claimed by %s and %s: %w",
				key, existing, goPath, ErrConflictingFields))
			continue
		}
		w.seen[key] = goPath

		w.fields = append(w.fields, fieldDescriptor{
			Index:        index,
			GoName:       f.Name,
			GoPath:       goPath,
			Key:          key,
			Description:  envDesc,
			Required:     tag.Required,
			NotEmpty:     tag.NotEmpty,
			HasDefault:   tag.HasDefault,
			DefaultValue: tag.DefaultValue,
			Separator:    tag.Separator,
			KVSeparator:  tag.KVSeparator,
			Expand:       tag.Expand,
			FromFile:     tag.FromFile,
			Unset:        tag.Unset,
			Secret:       tag.Secret,
			Immutable:    tag.Immutable,
			Layout:       tag.Layout,
			Base64:       tag.Base64,
			Hex:          tag.Hex,
		})
	}
}

// fieldTag is the parsed body of one `env:"..."` tag.
type fieldTag struct {
	Name string

	Required     bool
	NotEmpty     bool
	HasDefault   bool
	DefaultValue string
	Separator    string
	KVSeparator  string
	Expand       bool
	FromFile     bool
	Unset        bool
	Secret       bool
	Immutable    bool
	Layout       string
	Base64       bool
	Hex          bool
}

// parseFieldTag parses the body of an `env:"..."` tag. The "-" skip
// case is filtered by the caller. Option values cannot contain commas
// (reserved by the option list); use the envDesc companion tag for
// free-text content.
func parseFieldTag(raw string) (fieldTag, error) {
	t := fieldTag{}
	if raw == "" {
		return t, nil
	}

	parts := strings.Split(raw, ",")
	t.Name = parts[0]

	for _, p := range parts[1:] {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		key, value, hasValue := strings.Cut(p, "=")
		if hasValue {
			switch key {
			case "default":
				t.HasDefault = true
				t.DefaultValue = value
			case "separator":
				t.Separator = value
			case "kvSeparator":
				t.KVSeparator = value
			case "layout":
				t.Layout = value
			default:
				return t, fmt.Errorf("%w: %q", ErrUnknownTagOption, key)
			}
			continue
		}
		switch key {
		case "required":
			t.Required = true
		case "notEmpty":
			t.NotEmpty = true
		case "expand":
			t.Expand = true
		case "fromFile":
			t.FromFile = true
		case "unset":
			t.Unset = true
		case "secret":
			t.Secret = true
		case "immutable":
			t.Immutable = true
		case "base64":
			t.Base64 = true
		case "hex":
			t.Hex = true
		default:
			return t, fmt.Errorf("%w: %q", ErrUnknownTagOption, key)
		}
	}
	return t, nil
}

// toScreamingSnake transforms a Go field name into SCREAMING_SNAKE_CASE
// with acronym detection:
//
//	Port         -> PORT
//	DatabaseURL  -> DATABASE_URL
//	HTTPSPort    -> HTTPS_PORT
//	IPv4Address  -> IPV4_ADDRESS
//	URLs         -> URLS
//	camelCase    -> CAMEL_CASE
//
// Underscore is inserted (1) between a lowercase/digit and a following
// uppercase, and (2) between two uppercases when the second uppercase
// begins a run of two or more lowercase letters (a real word, not a
// trailing-acronym suffix).
func toScreamingSnake(name string) string {
	if name == "" {
		return ""
	}
	runes := []rune(name)
	var b strings.Builder
	b.Grow(len(name) + 4)
	for i, r := range runes {
		if i > 0 {
			prev := runes[i-1]
			switch {
			case (unicode.IsLower(prev) || unicode.IsDigit(prev)) && unicode.IsUpper(r):
				b.WriteByte('_')
			case unicode.IsUpper(prev) && unicode.IsUpper(r):
				if i+2 < len(runes) && unicode.IsLower(runes[i+1]) && unicode.IsLower(runes[i+2]) {
					b.WriteByte('_')
				}
			}
		}
		b.WriteRune(unicode.ToUpper(r))
	}
	return b.String()
}

// builtinLeafStructTypes are struct types the walker treats as opaque
// leaves. Listed explicitly for performance and to cover types like
// [net.IPNet] that implement no unmarshaler interface.
var builtinLeafStructTypes = map[reflect.Type]struct{}{
	reflect.TypeFor[time.Time]():     {},
	reflect.TypeFor[time.Location](): {},
	reflect.TypeFor[url.URL]():       {},
	reflect.TypeFor[net.IPNet]():     {},
	reflect.TypeFor[regexp.Regexp](): {},
	reflect.TypeFor[big.Int]():       {},
	reflect.TypeFor[big.Float]():     {},
	reflect.TypeFor[big.Rat]():       {},
}

var (
	textUnmarshalerType   = reflect.TypeFor[encoding.TextUnmarshaler]()
	binaryUnmarshalerType = reflect.TypeFor[encoding.BinaryUnmarshaler]()
)

// isLeafStruct reports whether t should be treated as an opaque leaf:
// either it's in the built-in registry, or *T implements
// [encoding.TextUnmarshaler] or [encoding.BinaryUnmarshaler]. (*T
// suffices: Go's method sets promote value-receiver methods.)
func isLeafStruct(t reflect.Type) bool {
	if _, ok := builtinLeafStructTypes[t]; ok {
		return true
	}
	pt := reflect.PointerTo(t)
	return pt.Implements(textUnmarshalerType) || pt.Implements(binaryUnmarshalerType)
}
