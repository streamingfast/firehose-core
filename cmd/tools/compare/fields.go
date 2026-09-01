package compare

import (
	"bytes"
	"fmt"
	"slices"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const maxPrintedFieldDiffs = 40

// diffFieldPaths returns protobuf field paths that differ between reference and
// current. Paths use proto field names (e.g. "header.chain_id", "txs[2]",
// "unknown_field(12)"). Nil or invalid messages are reported as a single path.
func diffFieldPaths(reference, current proto.Message) []string {
	switch {
	case reference == nil && current == nil:
		return nil
	case reference == nil, current == nil:
		return []string{"<nil message>"}
	}

	a := reference.ProtoReflect()
	b := current.ProtoReflect()
	switch {
	case a.IsValid() && b.IsValid():
		var out []string
		diffMessages(a, b, "", &out)
		return out
	case a.IsValid() != b.IsValid():
		return []string{"<invalid message>"}
	default:
		return nil
	}
}

func formatFieldDiffs(paths []string) []string {
	if len(paths) <= maxPrintedFieldDiffs {
		return paths
	}
	return append(slices.Clone(paths[:maxPrintedFieldDiffs]), fmt.Sprintf("... and %d more", len(paths)-maxPrintedFieldDiffs))
}

func diffMessages(a, b protoreflect.Message, prefix string, out *[]string) {
	if a.Descriptor() != b.Descriptor() {
		appendPath(out, prefixOr(prefix, "<descriptor mismatch>"))
		return
	}

	seen := make(map[protoreflect.FieldNumber]struct{})
	a.Range(func(fd protoreflect.FieldDescriptor, va protoreflect.Value) bool {
		seen[fd.Number()] = struct{}{}
		path := joinPath(prefix, string(fd.Name()))
		if !b.Has(fd) {
			appendPath(out, path)
			return true
		}
		diffValues(fd, va, b.Get(fd), path, out)
		return true
	})
	b.Range(func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		if _, ok := seen[fd.Number()]; ok {
			return true
		}
		appendPath(out, joinPath(prefix, string(fd.Name())))
		return true
	})

	diffUnknown(a.GetUnknown(), b.GetUnknown(), prefix, out)
}

func diffValues(fd protoreflect.FieldDescriptor, va, vb protoreflect.Value, path string, out *[]string) {
	switch {
	case fd.IsList():
		diffLists(fd, va.List(), vb.List(), path, out)
	case fd.IsMap():
		diffMaps(fd, va.Map(), vb.Map(), path, out)
	case fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind:
		if va.Equal(vb) {
			return
		}
		diffMessages(va.Message(), vb.Message(), path, out)
	default:
		if !va.Equal(vb) {
			appendPath(out, path)
		}
	}
}

func diffLists(fd protoreflect.FieldDescriptor, la, lb protoreflect.List, path string, out *[]string) {
	if protoreflect.ValueOfList(la).Equal(protoreflect.ValueOfList(lb)) {
		return
	}
	if la.Len() != lb.Len() {
		appendPath(out, fmt.Sprintf("%s: length %d != %d", path, la.Len(), lb.Len()))
	}
	n := min(la.Len(), lb.Len())
	elemIsMessage := fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind
	for i := range n {
		va, vb := la.Get(i), lb.Get(i)
		elemPath := fmt.Sprintf("%s[%d]", path, i)
		if elemIsMessage {
			if va.Equal(vb) {
				continue
			}
			diffMessages(va.Message(), vb.Message(), elemPath, out)
			continue
		}
		if !va.Equal(vb) {
			appendPath(out, elemPath)
		}
	}
}

func diffMaps(fd protoreflect.FieldDescriptor, ma, mb protoreflect.Map, path string, out *[]string) {
	if protoreflect.ValueOfMap(ma).Equal(protoreflect.ValueOfMap(mb)) {
		return
	}
	elemIsMessage := fd.MapValue().Kind() == protoreflect.MessageKind || fd.MapValue().Kind() == protoreflect.GroupKind
	ma.Range(func(k protoreflect.MapKey, va protoreflect.Value) bool {
		elemPath := fmt.Sprintf("%s[%s]", path, k.String())
		if !mb.Has(k) {
			appendPath(out, elemPath)
			return true
		}
		vb := mb.Get(k)
		if elemIsMessage {
			if va.Equal(vb) {
				return true
			}
			diffMessages(va.Message(), vb.Message(), elemPath, out)
			return true
		}
		if !va.Equal(vb) {
			appendPath(out, elemPath)
		}
		return true
	})
	mb.Range(func(k protoreflect.MapKey, _ protoreflect.Value) bool {
		if ma.Has(k) {
			return true
		}
		appendPath(out, fmt.Sprintf("%s[%s]", path, k.String()))
		return true
	})
}

func diffUnknown(aUnknown, bUnknown protoreflect.RawFields, prefix string, out *[]string) {
	if bytes.Equal(aUnknown, bUnknown) {
		return
	}

	aFields := unknownByNumber(aUnknown)
	bFields := unknownByNumber(bUnknown)
	var nums []protowire.Number
	for num := range aFields {
		nums = append(nums, num)
	}
	for num := range bFields {
		if _, ok := aFields[num]; !ok {
			nums = append(nums, num)
		}
	}
	slices.Sort(nums)

	found := false
	for _, num := range nums {
		if bytes.Equal(aFields[num], bFields[num]) {
			continue
		}
		found = true
		appendPath(out, joinPath(prefix, fmt.Sprintf("unknown_field(%d)", num)))
	}
	if !found {
		appendPath(out, joinPath(prefix, "unknown_fields"))
	}
}

func unknownByNumber(raw protoreflect.RawFields) map[protowire.Number][]byte {
	out := make(map[protowire.Number][]byte)
	b := []byte(raw)
	for len(b) > 0 {
		num, _, n := protowire.ConsumeField(b)
		if n < 0 {
			out[0] = append(out[0], b...)
			break
		}
		out[num] = append(out[num], b[:n]...)
		b = b[n:]
	}
	return out
}

func joinPath(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

func prefixOr(prefix, fallback string) string {
	if prefix == "" {
		return fallback
	}
	return prefix
}

func appendPath(out *[]string, path string) {
	*out = append(*out, path)
}
