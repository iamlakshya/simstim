package tomlinspect

const (
	metadataRegionSlots = 5
	objectRegionSlot    = 4
)

// Slot zero is the live TOML graph. Slots one to three are the current
// public-state relations, current symbolic runs and current grammar.
// Slot four is a transient object-name index, retired before its visitor.
// This private physical layer grants no source/role/permission capability.
type regionOwner struct {
	quota              uint64
	charged            bool
	generation         uint64
	first, last, pages uint32
	uses               uint32
	active             bool
}

type regionPages struct {
	epoch               uint64
	pages               *indexPages
	owners              [metadataRegionSlots]regionOwner
	freeHead, freeCount uint32
	failure             error
}

type regionRef struct {
	epoch      uint64
	allocator  *regionPages
	generation uint64
	slot       uint16
}

func (r *regionPages) reset(pages *indexPages) error {
	if r == nil || r.pages != nil || r.epoch == ^uint64(0) || pages == nil || pages.ctx == nil || pages.file == nil || len(pages.data) != indexCacheBytes || pages.nextPage == 0 {
		return ErrIndexInvariant
	}
	if err := pages.ctx.Err(); err != nil {
		return err
	}
	*r = regionPages{pages: pages, epoch: r.epoch + 1}
	// Existing TOML pages already have owner zero in reserved header bytes.
	// They are never individually retired before the whole lease closes.
	r.owners[0] = regionOwner{generation: 1, active: true}
	return nil
}

func (r *regionPages) fail(err error) error {
	if err == nil {
		return nil
	}
	if r == nil {
		return ErrIndexInvariant
	}
	if r.failure == nil {
		r.failure = err
	}
	return r.failure
}

func (r *regionPages) stopped() error {
	if r == nil || r.pages == nil || r.pages.ctx == nil {
		return ErrIndexInvariant
	}
	if r.failure != nil {
		return r.failure
	}
	if err := r.pages.ctx.Err(); err != nil {
		return r.fail(err)
	}
	return nil
}

func (r *regionPages) begin(slot uint16) (regionRef, error) {
	if err := r.stopped(); err != nil {
		return regionRef{}, err
	}
	if slot == 0 || slot >= metadataRegionSlots {
		return regionRef{}, r.fail(ErrIndexInvariant)
	}
	owner := &r.owners[slot]
	if owner.active || owner.first != 0 || owner.last != 0 || owner.pages != 0 || owner.uses != 0 || owner.generation == ^uint64(0) {
		return regionRef{}, r.fail(ErrIndexInvariant)
	}
	generation := owner.generation + 1
	*owner = regionOwner{generation: generation, active: true}
	return regionRef{allocator: r, epoch: r.epoch, generation: owner.generation, slot: slot}, nil
}

func (r *regionPages) check(ref regionRef) error {
	if err := r.stopped(); err != nil {
		return err
	}
	if ref.allocator != r || ref.epoch != r.epoch || ref.slot == 0 || ref.slot >= metadataRegionSlots {
		return r.fail(ErrIndexInvariant)
	}
	owner := &r.owners[ref.slot]
	if !owner.active || owner.generation != ref.generation {
		return r.fail(ErrIndexInvariant)
	}
	return nil
}

// enter/leave cover complete public record/tree operations, including their
// page-free callback intervals. Zero cache pins alone does not mean quiescent.
func (r *regionPages) enter(ref regionRef) error {
	if err := r.check(ref); err != nil {
		return err
	}
	owner := &r.owners[ref.slot]
	if owner.uses == ^uint32(0) {
		return r.fail(ErrIndexInvariant)
	}
	owner.uses++
	return nil
}
func (r *regionPages) leave(ref regionRef) {
	if r == nil || ref.allocator != r || ref.epoch != r.epoch || ref.slot == 0 || ref.slot >= metadataRegionSlots {
		if r != nil {
			r.fail(ErrIndexInvariant)
		}
		return
	}
	owner := &r.owners[ref.slot]
	if owner.generation != ref.generation || owner.uses == 0 {
		r.fail(ErrIndexInvariant)
		return
	}
	owner.uses--
}

func (r *regionPages) pin(ref regionRef, id uint32) ([]byte, int, error) {
	if err := r.check(ref); err != nil {
		return nil, 0, err
	}
	data, slot, err := r.pages.pin(id)
	if err != nil {
		return nil, 0, r.fail(err)
	}
	if read16(data, 10) != ref.slot {
		r.pages.release(slot, false)
		return nil, 0, r.fail(ErrIndexCorrupt)
	}
	return data, slot, nil
}

// detach is called only after all complete operations and borrowed data have
// ended, before the sole lease closes. Retain only the generation counter so
// a later reset cannot adopt a prior inspection's physical region reference.
func (r *regionPages) detach() {
	if r != nil {
		*r = regionPages{epoch: r.epoch}
	}
}

func (r *regionPages) setCharge(ref regionRef, pages uint64) error {
	if err := r.check(ref); err != nil {
		return err
	}
	owner := &r.owners[ref.slot]
	if owner.charged || owner.pages != 0 || owner.uses != 0 {
		return r.fail(ErrIndexInvariant)
	}
	owner.quota, owner.charged = pages, true
	return nil
}
