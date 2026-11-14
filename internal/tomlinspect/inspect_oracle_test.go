//go:build darwin || linux

package tomlinspect

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

type nativeOracleVisitor struct{ want map[string]any }

func (v nativeOracleVisitor) EnterTable(_ PathCursor, table TableCursor) error {
	return compareFinalNative(table.Value(), v.want)
}
func (nativeOracleVisitor) LeaveTable(PathCursor) error              { return nil }
func (nativeOracleVisitor) EnterArray(PathCursor, ArrayCursor) error { return ErrSyntax }
func (nativeOracleVisitor) LeaveArray(PathCursor) error              { return ErrSyntax }
func (nativeOracleVisitor) Scalar(PathCursor, ScalarCursor) error    { return ErrSyntax }
func (nativeOracleVisitor) Finish() error                            { return nil }

// The oracle is the actual pinned decoder. The engine's final values are
// compared as borrowed cursors; no second engine result tree is built.
func compareFinalNative(value ValueCursor, want any) error {
	switch expected := want.(type) {
	case map[string]any:
		table, err := value.Table()
		if err != nil {
			return err
		}
		count := 0
		err = table.Walk(func(path PathCursor, child ValueCursor) error {
			name, err := path.Name()
			if err != nil {
				return err
			}
			for candidate, datum := range expected {
				equal, err := name.Equal(candidate)
				if err != nil {
					return err
				}
				if equal {
					count++
					return compareFinalNative(child, datum)
				}
			}
			return fmt.Errorf("unexpected final key")
		})
		if err == nil && count != len(expected) {
			return fmt.Errorf("final table key count differs")
		}
		return err
	case []any:
		array, err := value.Array()
		if err != nil {
			return err
		}
		length, err := array.Len()
		if err != nil || int(length) != len(expected) {
			return fmt.Errorf("final array length differs: %w", err)
		}
		return array.Walk(func(index uint32, _ PathCursor, child ValueCursor) error {
			return compareFinalNative(child, expected[index])
		})
	case []map[string]any:
		array, err := value.Array()
		if err != nil {
			return err
		}
		length, err := array.Len()
		if err != nil || int(length) != len(expected) {
			return fmt.Errorf("final table-array length differs: %w", err)
		}
		return array.Walk(func(index uint32, _ PathCursor, child ValueCursor) error {
			return compareFinalNative(child, expected[index])
		})
	default:
		scalar, err := value.Scalar()
		if err != nil {
			return err
		}
		switch expected := expected.(type) {
		case string:
			text, ok := scalar.Text()
			if !ok {
				return fmt.Errorf("final scalar string type differs")
			}
			equal, err := text.Equal(expected)
			if err != nil {
				return err
			}
			if !equal {
				return fmt.Errorf("final scalar string differs")
			}
		case int64:
			actual, ok := scalar.Integer()
			if !ok || actual != expected {
				return fmt.Errorf("final integer differs")
			}
		case float64:
			actual, ok := scalar.Float()
			if !ok || math.Float64bits(actual) != math.Float64bits(expected) {
				return fmt.Errorf("final float bits differ")
			}
		case bool:
			actual, ok := scalar.Bool()
			if !ok || actual != expected {
				return fmt.Errorf("final boolean differs")
			}
		case time.Time:
			actual, ok := scalar.Datetime()
			if !ok {
				return fmt.Errorf("final date type differs")
			}
			aName, aOffset := actual.Zone()
			wName, wOffset := expected.Zone()
			if !actual.Equal(expected) || aName != wName || aOffset != wOffset {
				return fmt.Errorf("final date/zone differs")
			}
		default:
			return fmt.Errorf("unsupported native oracle type %T", expected)
		}
		return nil
	}
}

func TestOwnedMetadataInspectMatchesWholePinnedDecoder(t *testing.T) {
	factory := fixtureIndex(t)
	inspector, err := NewInspector(factory)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{
		"", "a=1\n", "a=1\na=2\n", "a=1\na.b=2\n", "a.b=1\n[a]\nc=2\n",
		"a=[]\na.b=1\n", "[a]\nb=1\n[a]\n", "[a.b]\nc=1\n[a]\nd=2\n",
		"a={\"\"=[]}\na.\"\".x=1\n", "a={x.y=1,x=2,\"\"=[]}\na.x.z=3\n",
		"a={x.y=1,x=2,x.z=3}\n", "a={x.y=1,x=2}\n", "a={x.y=1,x=2,\"\"=[]}\n",
		"ui={motion.extra=1,motion=\"auto\",\"\"=[]}\n", "a={x={old=1},x.new=2,\"\"=[]}\n",
		"a={x=[{z=1},{z=2}],\"\"=[]}\n", "a={x=[{}],x.y=1}\n",
		"a=[[2],{\"\"=1}]\n", "[t]\n\"\"=[{x=1},2]\n",
		"ui.motion='bad'\nui.\"\"=[]\nui.motion='auto'\n",
		"ui.motion='bad'\nui.\"\"=[]\n[ui.sub]\nx=1\n",
		"a=[[{b=1}],[{b=2}]]\n", "v=[1,[2,{x=3}],{x=[{y=4},{y=5}]}]\n",
		"[[a]]\nx=1\n[[a]]\nx=2\n", "[[a]]\nx=1\nx=2\n", "[a]\nx=1\n[[a]]\n",
		"[[a]]\n[a.b]\nx=1\n[[a]]\n[a.b]\nx=2\n", "[[a.b]]\nx=1\n[[a]]\nx=2\n",
		"\"\"=0\n", "[a]\n\"\"=[]\n", "[a]\n\"\"=[{}]\n", "[a]\n\"\"={b=1}\n",
		"\"a.b\"=1\na.b=2\n", "\"a\\u0062\"=1\nab=2\n", "\"a\"=1\nA=2\n",
		"v=-nan\nx=-0.0\ny=5e-324\n", "v=1979-05-27T07:32:00.123456789123Z\n",
		"v=\"\\uD800\"\n", "unknown={deep=[999999999999999999999999]}\n", "#literal �\nv='�'\n",
		"v=\"\\e\\xFF\\x00\"\n", "\xef\xbb\xbfk='é'\n", "\xff\xfek=1\n", "\xfe\xffk=1\n",
		string([]byte{'#', 0xff, '\n'}), "v=\"\"\"line\\\n  kept\"\"\"\n",
	} {
		var expected map[string]any
		_, oracleErr := toml.Decode(input, &expected)
		visitor := SchemaVisitor(syntaxOnlyVisitor{})
		if oracleErr == nil {
			visitor = nativeOracleVisitor{want: expected}
		}
		actualErr := inspector.Inspect(t.Context(), strings.NewReader(input), int64(len(input)), visitor)
		if (actualErr == nil) != (oracleErr == nil) {
			t.Fatalf("whole pinned acceptance/final value differs at input length %d: oracle %v, metadata %v", len(input), oracleErr, actualErr)
		}
		if factory.opened != factory.closed {
			t.Fatal("an inspection retained its owned index lease")
		}
	}
}
