package tomlinspect

// These are source-derived physical counters, never wire-declared quotas.
// The actual count-token constructor and source checks must seal them before
// a region begins. A failed legal-source theorem is an implementation defect.
type stateRecordCounts struct{ entries, bindings, hosts, slots uint64 }
type entryRecordCounts struct{ runs, runRows, gridBits, frontierIDs uint64 }
type regionPageCharges struct{ global, runs, grammar uint64 }
type grammarPositionCounts struct{ bits, seen, frontier uint64 }

func chargeAdd(a, b uint64) (uint64, error) {
	if a > ^uint64(0)-b {
		return 0, ErrIndexInvariant
	}
	return a + b, nil
}
func chargeMul(a, b uint64) (uint64, error) {
	if b != 0 && a > ^uint64(0)/b {
		return 0, ErrIndexInvariant
	}
	return a * b, nil
}
func chargeCeil(n, d uint64) (uint64, error) {
	if d == 0 {
		return 0, ErrIndexInvariant
	}
	q := n / d
	if n%d != 0 {
		return chargeAdd(q, 1)
	}
	return q, nil
}
func chargeSum(values ...uint64) (uint64, error) {
	var sum uint64
	for _, value := range values {
		var err error
		sum, err = chargeAdd(sum, value)
		if err != nil {
			return 0, err
		}
	}
	return sum, nil
}

// packedRecordPages is D_w(c), including the actual shared two-level root
// and leaf directories. Only fixed metadata widths are admitted by callers.
func packedRecordPages(width, count uint64) (uint64, error) {
	if width == 0 || width > indexPageBytes-indexPageHeader {
		return 0, ErrIndexInvariant
	}
	if count == 0 {
		return 0, nil
	}
	perPage := uint64(indexPageBytes-indexPageHeader) / width
	data, err := chargeCeil(count, perPage)
	if err != nil {
		return 0, err
	}
	if data > directorySlots*directorySlots {
		return 0, ErrIndexInvariant
	}
	leaves, err := chargeCeil(data, directorySlots)
	if err != nil {
		return 0, err
	}
	return chargeSum(data, leaves, 1)
}

// numericTreePages is B(c). Min non-root leaf occupancy is63; non-root
// branch children are at least127. L=1+sum(children-1) bounds branch pages.
func numericTreePages(count uint64) (uint64, error) {
	if count == 0 {
		return 0, nil
	}
	leaves, err := chargeCeil(count, leafCapacity/2)
	if err != nil {
		return 0, err
	}
	if leaves == 1 {
		return 1, nil
	}
	branches := 1 + (leaves-2)/(branchCapacity/2-1)
	return chargeAdd(leaves, branches)
}

// bitmapPages is M(h):4064 payload bytes per SAME directory data page.
// It does not reserve a byte/record or a Go slice per individual grid bit.
func bitmapPages(bits uint64) (uint64, error) {
	bytes, err := chargeCeil(bits, 8)
	if err != nil {
		return 0, err
	}
	return packedRecordPages(1, bytes)
}

// partialTreePages follows actual put: one new leaf, one per saved ancestor,
// then one new root. No comparator may allocate or recursively enter a tree.
const partialTreePages = len((treePath{}).steps) + 2

func statePhysicalCharge(c stateRecordCounts) (uint64, error) {
	e, err := packedRecordPages(192, c.entries)
	if err != nil {
		return 0, err
	}
	b, err := packedRecordPages(192, c.bindings)
	if err != nil {
		return 0, err
	}
	h, err := packedRecordPages(96, c.hosts)
	if err != nil {
		return 0, err
	}
	t, err := packedRecordPages(96, c.slots)
	if err != nil {
		return 0, err
	}
	entries, err := numericTreePages(c.entries)
	if err != nil {
		return 0, err
	}
	hosts, err := numericTreePages(c.hosts)
	if err != nil {
		return 0, err
	}
	partial := uint64(0)
	if c.entries != 0 || c.hosts != 0 {
		partial = uint64(partialTreePages)
	}
	return chargeSum(e, b, h, t, entries, entries, hosts, partial)
}

func entryPhysicalCharge(c entryRecordCounts) (regionPageCharges, error) {
	var result regionPageCharges
	runs, err := packedRecordPages(96, c.runs)
	if err != nil {
		return result, err
	}
	rows, err := numericTreePages(c.runRows)
	if err != nil {
		return result, err
	}
	partial := uint64(0)
	if c.runRows != 0 {
		partial = uint64(partialTreePages)
	}
	result.runs, err = chargeSum(runs, rows, partial)
	if err != nil {
		return result, err
	}
	bitmap, err := bitmapPages(c.gridBits)
	if err != nil {
		return result, err
	}
	blocks, err := chargeCeil(c.frontierIDs, 8)
	if err != nil {
		return result, err
	}
	frontier, err := packedRecordPages(64, blocks)
	if err != nil {
		return result, err
	}
	result.grammar, err = chargeAdd(bitmap, frontier)
	return result, err
}

// grammarPhysicalCounts recomputes SAME compiler facts. work is the actual
// canonical work constant supplied by its producer, never a wire field or
// independently selected backend cap. Source-role/token validation is separate.
func grammarPhysicalCounts(instructions, width, alternatives, work uint64) (grammarPositionCounts, error) {
	var result grammarPositionCounts
	if instructions == 0 || work == 0 || alternatives > instructions {
		return result, ErrIndexInvariant
	}
	stride, err := chargeAdd(width, 1)
	if err != nil {
		return result, err
	}
	cells, err := chargeMul(instructions, stride)
	if err != nil {
		return result, err
	}
	result.bits, err = chargeMul(cells, 4)
	if err != nil {
		return result, err
	}
	result.seen = min(result.bits, work)
	branches, err := chargeMul(alternatives, stride)
	if err != nil {
		return result, err
	}
	branches, err = chargeMul(branches, 4)
	if err != nil {
		return result, err
	}
	frontier, err := chargeAdd(branches, 1)
	if err != nil {
		return result, err
	}
	result.frontier = min(result.seen, frontier)
	return result, nil
}
