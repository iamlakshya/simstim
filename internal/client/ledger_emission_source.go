package client

import "github.com/iamlakshya/simstim/internal/tomlinspect"

// ledgerEmissionSource is issued only after the complete ledger projection
// succeeds inside this actual inspection. It borrows the SAME parser/source
// and retains no decoded history or new tree. Emission copies validated
// original spans and splices only the known array and selected value spans.
type ledgerEmissionSource struct {
	ledgerValueProjection
	ledger     *projectedLedger
	guarded    *agentJSONProjection
	inspection *tomlinspect.Inspection
	source     tomlinspect.MetadataSource
}

func newLedgerEmissionSource(h *tomlinspect.Inspection, guarded *agentJSONProjection, host Host, hostID string) (*ledgerEmissionSource, error) {
	if h == nil || guarded == nil || guarded.inspection != h {
		return nil, tomlinspect.ErrIndexInvariant
	}
	ctx, err := h.Context()
	if err != nil {
		return nil, err
	}
	// No DTO enters this constructor. It executes the complete original
	// semantic pass itself before issuing the private emission borrower.
	ledger, err := projectLedgerProjection(ctx, guarded, host, hostID)
	if err != nil {
		return nil, err
	}
	source, err := h.Source(1)
	if err != nil {
		return nil, err
	}
	p := &ledgerEmissionSource{ledgerValueProjection: ledger.p, ledger: ledger, guarded: guarded, inspection: h, source: source}
	if err := p.check(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *ledgerEmissionSource) check() error {
	if p == nil || p.inspection == nil || p.guarded == nil || p.ledger == nil || p.ledger.p != p.guarded || p.guarded.inspection != p.inspection {
		return tomlinspect.ErrIndexInvariant
	}
	ctx, err := p.inspection.Context()
	if err != nil {
		return err
	}
	source, err := p.inspection.Source(1)
	if err != nil {
		return err
	}
	if source != p.source || source.Kind() != tomlinspect.MetadataStateJSON || !p.guarded.JSONProjection.MatchesSource(ctx, source, source.Size()) {
		return tomlinspect.ErrIndexInvariant
	}
	return source.Check(ctx)
}
