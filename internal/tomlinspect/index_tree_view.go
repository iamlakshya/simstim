package tomlinspect

// numericTrees borrows the SAME page store and fixed root catalogue. It
// contains no source, TOML object, name dictionary or acquired lifetime.
// The current TOML owner keeps the roots; future admitted regions must keep
// their own roots and ownership guards while borrowing these same methods.
type numericTrees struct {
	region *regionPages
	ref    regionRef
	*indexPages
	roots         *[treeCount]uint32
	comparison    numericComparison
	prefixGrouped bool
}

func (s *indexStore) treeView() numericTrees {
	return numericTrees{indexPages: &s.indexPages, roots: &s.roots}
}

func (s *indexStore) get(tree byte, key numericKey) (numericValue, bool, error) {
	trees := s.treeView()
	return trees.get(tree, key)
}

func (s *indexStore) put(tree byte, key numericKey, value numericValue) error {
	trees := s.treeView()
	return trees.put(tree, key, value)
}

func (s *indexStore) walk(tree byte, first uint32, visit func(numericKey, numericValue) error) error {
	trees := s.treeView()
	return trees.walk(tree, first, visit)
}

func (s *indexStore) descend(tree byte, key numericKey, path *treePath) (uint32, error) {
	trees := s.treeView()
	return trees.descend(tree, key, path)
}

// numericComparison is only an exact metadata/source key strategy. It is
// never a permission provider. Its caller owns all source/region lifetimes;
// compare may read fixed records/cursors but cannot recursively use a tree,
// allocate/write metadata, retain page views or change indexed key fields.
type numericComparison interface {
	compare(numericKey, numericKey) (int, error)
}

func (s *numericTrees) order(a, b numericKey) (int, error) {
	if s.comparison == nil {
		return compareKey(a, b), nil
	}
	return s.comparison.compare(a, b)
}

// prefixGrouped is set only by a typed region adapter whose exact order is
// numeric first-word prefix followed by source-key order. Its second-word
// zero is the lower-bound query sentinel, never an inserted record. This
// property is NOT available to an unconstrained source comparison strategy.
