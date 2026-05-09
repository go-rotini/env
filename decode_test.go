package env

import (
	"context"
	"errors"
	"math/big"
	"net"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"testing"
	"time"
)

// decodeInto is a test helper: allocates a fresh *T, runs decodeValue,
// and returns the populated value plus any error.
func decodeInto[T any](t *testing.T, text string, opts decodeOpts) (T, error) {
	t.Helper()
	var zero T
	holder := reflect.New(reflect.TypeFor[T]())
	err := decodeValue(holder.Elem(), text, opts)
	if err != nil {
		return zero, err
	}
	v, _ := holder.Elem().Interface().(T)
	return v, nil
}

// --- primitives ---

func TestDecode_String(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"hello", "hello"},
		{"with spaces and #chars", "with spaces and #chars"},
	}
	for _, tc := range cases {
		got, err := decodeInto[string](t, tc.in, decodeOpts{})
		if err != nil {
			t.Errorf("decodeInto[string](%q) err: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("decodeInto[string](%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDecode_Bool(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"true", true}, {"True", true}, {"TRUE", true},
		{"1", true}, {"yes", true}, {"YES", true},
		{"on", true}, {"ON", true},
		{"false", false}, {"False", false}, {"FALSE", false},
		{"0", false}, {"no", false}, {"off", false},
	}
	for _, tc := range cases {
		got, err := decodeInto[bool](t, tc.in, decodeOpts{})
		if err != nil {
			t.Errorf("decodeInto[bool](%q) err: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("decodeInto[bool](%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestDecode_Bool_Invalid(t *testing.T) {
	for _, in := range []string{"", "maybe", "trueish", "2"} {
		_, err := decodeInto[bool](t, in, decodeOpts{})
		if err == nil {
			t.Errorf("decodeInto[bool](%q) accepted invalid input", in)
		}
		if !errors.Is(err, ErrParse) {
			t.Errorf("decodeInto[bool](%q) err = %v, want ErrParse", in, err)
		}
	}
}

func TestDecode_Int(t *testing.T) {
	v, err := decodeInto[int](t, "-42", decodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if v != -42 {
		t.Errorf("got %d", v)
	}
}

func TestDecode_AllIntKinds(t *testing.T) {
	if v, err := decodeInto[int8](t, "-1", decodeOpts{}); err != nil || v != -1 {
		t.Errorf("int8: %v, %v", v, err)
	}
	if v, err := decodeInto[int16](t, "-1234", decodeOpts{}); err != nil || v != -1234 {
		t.Errorf("int16: %v, %v", v, err)
	}
	if v, err := decodeInto[int32](t, "-100000", decodeOpts{}); err != nil || v != -100000 {
		t.Errorf("int32: %v, %v", v, err)
	}
	if v, err := decodeInto[int64](t, "-9223372036854775000", decodeOpts{}); err != nil || v != -9223372036854775000 {
		t.Errorf("int64: %v, %v", v, err)
	}
}

func TestDecode_AllUintKinds(t *testing.T) {
	if v, err := decodeInto[uint](t, "42", decodeOpts{}); err != nil || v != 42 {
		t.Errorf("uint: %v, %v", v, err)
	}
	if v, err := decodeInto[uint8](t, "255", decodeOpts{}); err != nil || v != 255 {
		t.Errorf("uint8: %v, %v", v, err)
	}
	if v, err := decodeInto[uint16](t, "65535", decodeOpts{}); err != nil || v != 65535 {
		t.Errorf("uint16: %v, %v", v, err)
	}
	if v, err := decodeInto[uint32](t, "4294967295", decodeOpts{}); err != nil || v != 4294967295 {
		t.Errorf("uint32: %v, %v", v, err)
	}
	if v, err := decodeInto[uint64](t, "18446744073709551615", decodeOpts{}); err != nil || v != 18446744073709551615 {
		t.Errorf("uint64: %v, %v", v, err)
	}
	if v, err := decodeInto[uintptr](t, "42", decodeOpts{}); err != nil || v != 42 {
		t.Errorf("uintptr: %v, %v", v, err)
	}
}

func TestDecode_IntOverflow(t *testing.T) {
	_, err := decodeInto[int8](t, "300", decodeOpts{})
	if err == nil {
		t.Fatal("expected overflow error")
	}
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse wrap", err)
	}
	// strconv.ErrRange should be reachable via Unwrap chain.
	if !errors.Is(err, strconv.ErrRange) {
		t.Errorf("err = %v, want unwraps to strconv.ErrRange", err)
	}
}

func TestDecode_IntInvalid(t *testing.T) {
	_, err := decodeInto[int](t, "abc", decodeOpts{})
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

func TestDecode_IntEmpty(t *testing.T) {
	// Numeric, bool, and time.Duration treat the empty string as a
	// parse error; for optionality, declare the field as a pointer.
	_, err := decodeInto[int](t, "", decodeOpts{})
	if err == nil {
		t.Fatal("expected error parsing empty string as int")
	}
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

func TestDecode_Float(t *testing.T) {
	if v, err := decodeInto[float32](t, "3.14", decodeOpts{}); err != nil || v < 3.13 || v > 3.15 {
		t.Errorf("float32: %v, %v", v, err)
	}
	if v, err := decodeInto[float64](t, "1e10", decodeOpts{}); err != nil || v != 1e10 {
		t.Errorf("float64: %v, %v", v, err)
	}
}

func TestDecode_FloatInvalid(t *testing.T) {
	_, err := decodeInto[float64](t, "not-a-number", decodeOpts{})
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

// --- pointer auto-init ---

func TestDecode_PointerInt(t *testing.T) {
	got, err := decodeInto[*int](t, "42", decodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("got nil pointer")
	}
	if *got != 42 {
		t.Errorf("got %d, want 42", *got)
	}
}

func TestDecode_PointerInt_EmptyAllocatesZero(t *testing.T) {
	// "Set with empty value" → pointer is allocated and points at the
	// zero value of T, regardless of whether parsing the empty string
	// would succeed for T directly.
	got, err := decodeInto[*int](t, "", decodeOpts{})
	if err != nil {
		t.Fatalf("expected success on *int with empty value, got: %v", err)
	}
	if got == nil {
		t.Fatal("got nil pointer; expected non-nil pointing at zero value")
	}
	if *got != 0 {
		t.Errorf("got %d, want 0", *got)
	}
}

func TestDecode_PointerString_EmptyAllocates(t *testing.T) {
	got, err := decodeInto[*string](t, "", decodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || *got != "" {
		t.Errorf("got %v, want non-nil pointer to empty string", got)
	}
}

func TestDecode_PointerBool(t *testing.T) {
	got, err := decodeInto[*bool](t, "true", decodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || !*got {
		t.Errorf("got %v, want pointer to true", got)
	}
}

func TestDecode_DoublePointer(t *testing.T) {
	got, err := decodeInto[**int](t, "42", decodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || *got == nil || **got != 42 {
		t.Errorf("got %v", got)
	}
}

// --- []byte / [N]byte ---

func TestDecode_ByteSlice_Raw(t *testing.T) {
	got, err := decodeInto[[]byte](t, "hello", decodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("got %q", got)
	}
}

func TestDecode_ByteSlice_Base64(t *testing.T) {
	got, err := decodeInto[[]byte](t, "aGVsbG8=", decodeOpts{Base64: true})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("got %q", got)
	}
}

func TestDecode_ByteSlice_Hex(t *testing.T) {
	got, err := decodeInto[[]byte](t, "68656c6c6f", decodeOpts{Hex: true})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("got %q", got)
	}
}

func TestDecode_ByteSlice_Base64Invalid(t *testing.T) {
	_, err := decodeInto[[]byte](t, "not-base64!", decodeOpts{Base64: true})
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

func TestDecode_ByteSlice_HexInvalid(t *testing.T) {
	_, err := decodeInto[[]byte](t, "zz", decodeOpts{Hex: true})
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

func TestDecode_ByteArray_Raw(t *testing.T) {
	got, err := decodeInto[[5]byte](t, "hello", decodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if string(got[:]) != "hello" {
		t.Errorf("got %q", got)
	}
}

func TestDecode_ByteArray_LengthMismatch(t *testing.T) {
	_, err := decodeInto[[5]byte](t, "hi", decodeOpts{})
	if err == nil {
		t.Fatal("expected length-mismatch error")
	}
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

func TestDecode_ByteArray_Hex(t *testing.T) {
	got, err := decodeInto[[5]byte](t, "68656c6c6f", decodeOpts{Hex: true})
	if err != nil {
		t.Fatal(err)
	}
	if string(got[:]) != "hello" {
		t.Errorf("got %q", got)
	}
}

// --- slices and arrays of non-byte ---

func TestDecode_StringSlice(t *testing.T) {
	got, err := decodeInto[[]string](t, "a,b,c", decodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Errorf("got %v", got)
	}
}

func TestDecode_StringSlice_CustomSeparator(t *testing.T) {
	got, err := decodeInto[[]string](t, "a;b;c", decodeOpts{Separator: ";"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Errorf("got %v", got)
	}
}

func TestDecode_IntSlice(t *testing.T) {
	got, err := decodeInto[[]int](t, "1,2,3", decodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []int{1, 2, 3}) {
		t.Errorf("got %v", got)
	}
}

func TestDecode_EmptySlice(t *testing.T) {
	got, err := decodeInto[[]string](t, "", decodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty slice", got)
	}
}

func TestDecode_Slice_ElementError(t *testing.T) {
	_, err := decodeInto[[]int](t, "1,bad,3", decodeOpts{})
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

func TestDecode_FixedArray(t *testing.T) {
	got, err := decodeInto[[3]int](t, "1,2,3", decodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got != [3]int{1, 2, 3} {
		t.Errorf("got %v", got)
	}
}

func TestDecode_FixedArray_LengthMismatch(t *testing.T) {
	_, err := decodeInto[[3]int](t, "1,2", decodeOpts{})
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

func TestDecode_FixedArray_ElementError(t *testing.T) {
	_, err := decodeInto[[3]int](t, "1,bad,3", decodeOpts{})
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

func TestDecode_UintInvalid(t *testing.T) {
	_, err := decodeInto[uint](t, "not-a-uint", decodeOpts{})
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

func TestDecode_UintNegative(t *testing.T) {
	_, err := decodeInto[uint8](t, "-1", decodeOpts{})
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

// --- maps ---

func TestDecode_StringStringMap(t *testing.T) {
	got, err := decodeInto[map[string]string](t, "k1:v1,k2:v2", decodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"k1": "v1", "k2": "v2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDecode_Map_CustomSeparators(t *testing.T) {
	got, err := decodeInto[map[string]int](t, "a=1;b=2", decodeOpts{Separator: ";", KVSeparator: "="})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"a": 1, "b": 2}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDecode_Map_Empty(t *testing.T) {
	got, err := decodeInto[map[string]int](t, "", decodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Error("got nil; want non-nil empty map")
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty map", got)
	}
}

func TestDecode_Map_MissingSeparator(t *testing.T) {
	_, err := decodeInto[map[string]string](t, "no-separator-here", decodeOpts{})
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

func TestDecode_Map_IntKey(t *testing.T) {
	got, err := decodeInto[map[int]string](t, "1:a,2:b", decodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]string{1: "a", 2: "b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDecode_Map_BadKey(t *testing.T) {
	_, err := decodeInto[map[int]string](t, "abc:v", decodeOpts{})
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

func TestDecode_Map_BadValue(t *testing.T) {
	_, err := decodeInto[map[string]int](t, "k:abc", decodeOpts{})
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

// --- built-in special types ---

func TestDecode_Duration(t *testing.T) {
	got, err := decodeInto[time.Duration](t, "1h30m", decodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	want := 90 * time.Minute
	if got != want {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDecode_Duration_Invalid(t *testing.T) {
	_, err := decodeInto[time.Duration](t, "not-a-duration", decodeOpts{})
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

func TestDecode_Time_DefaultRFC3339(t *testing.T) {
	got, err := decodeInto[time.Time](t, "2026-05-08T12:34:56Z", decodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 5, 8, 12, 34, 56, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDecode_Time_CustomLayout(t *testing.T) {
	got, err := decodeInto[time.Time](t, "2026-05-08", decodeOpts{Layout: "2006-01-02"})
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDecode_Time_LayoutMismatch(t *testing.T) {
	_, err := decodeInto[time.Time](t, "2026-05-08T12:00:00Z", decodeOpts{Layout: "2006-01-02"})
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

func TestDecode_Location(t *testing.T) {
	got, err := decodeInto[*time.Location](t, "America/New_York", decodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("got nil location")
	}
	if got.String() != "America/New_York" {
		t.Errorf("got %v", got)
	}
}

func TestDecode_Location_Invalid(t *testing.T) {
	_, err := decodeInto[*time.Location](t, "Mars/Olympus_Mons", decodeOpts{})
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

func TestDecode_URL(t *testing.T) {
	// url.URL implements BinaryUnmarshaler — reaches the interface chain.
	got, err := decodeInto[url.URL](t, "https://example.com/path", decodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Host != "example.com" || got.Path != "/path" {
		t.Errorf("got %+v", got)
	}
}

func TestDecode_PointerURL(t *testing.T) {
	got, err := decodeInto[*url.URL](t, "https://example.com/", decodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Host != "example.com" {
		t.Errorf("got %v", got)
	}
}

func TestDecode_NetIP(t *testing.T) {
	// net.IP implements TextUnmarshaler.
	got, err := decodeInto[net.IP](t, "127.0.0.1", decodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Errorf("got %v", got)
	}
}

func TestDecode_NetIP_Invalid(t *testing.T) {
	_, err := decodeInto[net.IP](t, "not-an-ip", decodeOpts{})
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

func TestDecode_NetIPNet(t *testing.T) {
	got, err := decodeInto[net.IPNet](t, "10.0.0.0/8", decodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "10.0.0.0/8" {
		t.Errorf("got %v", got)
	}
}

func TestDecode_NetIPNet_Pointer(t *testing.T) {
	got, err := decodeInto[*net.IPNet](t, "192.168.0.0/16", decodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.String() != "192.168.0.0/16" {
		t.Errorf("got %v", got)
	}
}

func TestDecode_NetIPNet_Invalid(t *testing.T) {
	_, err := decodeInto[net.IPNet](t, "not-a-cidr", decodeOpts{})
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

func TestDecode_Regexp(t *testing.T) {
	got, err := decodeInto[regexp.Regexp](t, "^foo[0-9]+$", decodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.MatchString("foo42") {
		t.Errorf("compiled regexp does not match expected input")
	}
}

func TestDecode_Regexp_Invalid(t *testing.T) {
	_, err := decodeInto[regexp.Regexp](t, "[unclosed", decodeOpts{})
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

func TestDecode_BigInt(t *testing.T) {
	// big.Int implements TextUnmarshaler.
	got, err := decodeInto[big.Int](t, "12345678901234567890", decodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "12345678901234567890" {
		t.Errorf("got %s", got.String())
	}
}

func TestDecode_BigFloat(t *testing.T) {
	got, err := decodeInto[big.Float](t, "3.14159265358979", decodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Text('g', 5) != "3.1416" {
		t.Errorf("got %s", got.Text('g', 5))
	}
}

// --- custom Unmarshaler interface ---

type customDecodeable struct {
	got string
}

func (c *customDecodeable) UnmarshalEnv(s string) error {
	c.got = "decoded:" + s
	return nil
}

func TestDecode_CustomUnmarshaler(t *testing.T) {
	got, err := decodeInto[customDecodeable](t, "hello", decodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got.got != "decoded:hello" {
		t.Errorf("got %q", got.got)
	}
}

type erroringUnmarshaler struct{}

func (*erroringUnmarshaler) UnmarshalEnv(string) error { return errors.New("boom") }

func TestDecode_CustomUnmarshalerError(t *testing.T) {
	_, err := decodeInto[erroringUnmarshaler](t, "anything", decodeOpts{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse wrap", err)
	}
}

// --- TextUnmarshaler-only type ---

type textOnly struct {
	raw string
}

func (t *textOnly) UnmarshalText(b []byte) error {
	t.raw = "text:" + string(b)
	return nil
}

func TestDecode_TextUnmarshaler(t *testing.T) {
	got, err := decodeInto[textOnly](t, "hello", decodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got.raw != "text:hello" {
		t.Errorf("got %q", got.raw)
	}
}

type badText struct{}

func (*badText) UnmarshalText([]byte) error { return errors.New("bad") }

func TestDecode_TextUnmarshalerError(t *testing.T) {
	_, err := decodeInto[badText](t, "x", decodeOpts{})
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse wrap", err)
	}
}

// --- BinaryUnmarshaler-only type ---

type binaryOnly struct {
	raw string
}

func (b *binaryOnly) UnmarshalBinary(data []byte) error {
	b.raw = "binary:" + string(data)
	return nil
}

func TestDecode_BinaryUnmarshaler(t *testing.T) {
	got, err := decodeInto[binaryOnly](t, "hello", decodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got.raw != "binary:hello" {
		t.Errorf("got %q", got.raw)
	}
}

type badBinary struct{}

func (*badBinary) UnmarshalBinary([]byte) error { return errors.New("bad") }

func TestDecode_BinaryUnmarshalerError(t *testing.T) {
	_, err := decodeInto[badBinary](t, "x", decodeOpts{})
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse wrap", err)
	}
}

// --- priority chain ---

// implementsAll exercises the priority chain: it implements Unmarshaler,
// TextUnmarshaler, AND BinaryUnmarshaler. Unmarshaler should win.
type implementsAll struct {
	via string
}

func (i *implementsAll) UnmarshalEnv(s string) error    { i.via = "env:" + s; return nil }
func (i *implementsAll) UnmarshalText(b []byte) error   { i.via = "text:" + string(b); return nil }
func (i *implementsAll) UnmarshalBinary(b []byte) error { i.via = "binary:" + string(b); return nil }

func TestDecode_PriorityChain_UnmarshalerWinsOverText(t *testing.T) {
	got, err := decodeInto[implementsAll](t, "x", decodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got.via != "env:x" {
		t.Errorf("priority broken: got %q, want %q", got.via, "env:x")
	}
}

// implementsTextAndBinary exercises the next pair: TextUnmarshaler wins
// over BinaryUnmarshaler.
type implementsTextAndBinary struct {
	via string
}

func (i *implementsTextAndBinary) UnmarshalText(b []byte) error {
	i.via = "text:" + string(b)
	return nil
}

func (i *implementsTextAndBinary) UnmarshalBinary(b []byte) error {
	i.via = "binary:" + string(b)
	return nil
}

func TestDecode_PriorityChain_TextWinsOverBinary(t *testing.T) {
	got, err := decodeInto[implementsTextAndBinary](t, "x", decodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got.via != "text:x" {
		t.Errorf("priority broken: got %q, want %q", got.via, "text:x")
	}
}

// timeWithLayoutOverride: a time.Time field with layout= should bypass the
// TextUnmarshaler chain entirely. Without layout=, TextUnmarshaler is used.
func TestDecode_PriorityChain_TimeLayoutOverridesInterface(t *testing.T) {
	// With layout=: tag-option override fires first.
	got, err := decodeInto[time.Time](t, "08-05-2026", decodeOpts{Layout: "02-01-2006"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Year() != 2026 || got.Month() != 5 || got.Day() != 8 {
		t.Errorf("layout override produced wrong value: %v", got)
	}
}

// --- unsupported types ---

func TestDecode_Unsupported_Chan(t *testing.T) {
	var c chan int
	v := reflect.ValueOf(&c).Elem()
	err := decodeValue(v, "anything", decodeOpts{})
	if !errors.Is(err, ErrUnsupportedType) {
		t.Errorf("err = %v, want ErrUnsupportedType", err)
	}
}

func TestDecode_Unsupported_Func(t *testing.T) {
	var f func()
	v := reflect.ValueOf(&f).Elem()
	err := decodeValue(v, "x", decodeOpts{})
	if !errors.Is(err, ErrUnsupportedType) {
		t.Errorf("err = %v, want ErrUnsupportedType", err)
	}
}

func TestDecode_Unsupported_Complex(t *testing.T) {
	var c complex64
	v := reflect.ValueOf(&c).Elem()
	err := decodeValue(v, "1+2i", decodeOpts{})
	if !errors.Is(err, ErrUnsupportedType) {
		t.Errorf("err = %v, want ErrUnsupportedType", err)
	}
}

func TestDecode_Unsupported_Interface(t *testing.T) {
	var x any
	v := reflect.ValueOf(&x).Elem()
	err := decodeValue(v, "x", decodeOpts{})
	if !errors.Is(err, ErrUnsupportedType) {
		t.Errorf("err = %v, want ErrUnsupportedType", err)
	}
}

func TestDecode_Unsupported_StructWithoutInterface(t *testing.T) {
	// A struct that doesn't implement any interface and isn't in the
	// built-in registry should produce ErrUnsupportedType.
	type noUnmarshalers struct{ X int }
	var s noUnmarshalers
	v := reflect.ValueOf(&s).Elem()
	err := decodeValue(v, "x", decodeOpts{})
	if !errors.Is(err, ErrUnsupportedType) {
		t.Errorf("err = %v, want ErrUnsupportedType", err)
	}
}

// --- context interface (defined; not invoked by Phase 4 decoder) ---

type ctxUnmarshaler struct{ got string }

func (c *ctxUnmarshaler) UnmarshalEnv(_ context.Context, s string) error {
	c.got = s
	return nil
}

func TestDecode_UnmarshalerContext_NotInvokedYet(t *testing.T) {
	// Phase 4 does not invoke UnmarshalerContext; that wiring lives in
	// Phase 5 (Loader.LoadContext). A type that ONLY implements
	// UnmarshalerContext (not Unmarshaler) currently falls through every
	// interface check and lands in default reflection. Because the type
	// is a struct with no interface satisfaction and no built-in
	// registration, the decoder should report ErrUnsupportedType.
	got, err := decodeInto[ctxUnmarshaler](t, "x", decodeOpts{})
	if !errors.Is(err, ErrUnsupportedType) {
		t.Errorf("err = %v, want ErrUnsupportedType (UnmarshalerContext is wired in Phase 5)", err)
	}
	_ = got
}

// --- option propagation through containers ---

func TestDecode_LayoutPropagatesToSliceElements(t *testing.T) {
	got, err := decodeInto[[]time.Time](t, "2026-01-01,2026-12-31", decodeOpts{Layout: "2006-01-02"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Year() != 2026 || got[1].Month() != 12 {
		t.Errorf("layout did not propagate: %v", got)
	}
}

func TestDecode_LayoutPropagatesToMapValues(t *testing.T) {
	got, err := decodeInto[map[string]time.Time](t, "a:2026-01-01,b:2026-12-31", decodeOpts{Layout: "2006-01-02"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries", len(got))
	}
	if got["a"].Year() != 2026 {
		t.Errorf("layout did not propagate to map values: %v", got)
	}
}

// --- decodeOpts tests ---

func TestSeparatorOr(t *testing.T) {
	if got := separatorOr(",", ";"); got != "," {
		t.Errorf("got %q, want \",\"", got)
	}
	if got := separatorOr("", ";"); got != ";" {
		t.Errorf("got %q, want \";\"", got)
	}
}
