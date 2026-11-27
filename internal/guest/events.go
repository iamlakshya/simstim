package guest

import "time"

// The holder's stop already uses grace plus four seconds. Keep the event
// drain and control close within the remaining second of stopSlack.
const (
	eventsDrainWait = 500 * time.Millisecond
	ctlCloseWait    = 250 * time.Millisecond
)

// drainHolderEvents lets buffered terminal claims reach ctl before it closes.
// A missing reader or inherited fd never gives shutdown an unbounded wait.
func (g *Init) drainHolderEvents(hp *holderProc) {
	if hp == nil {
		return
	}
	timer := time.NewTimer(eventsDrainWait)
	defer timer.Stop()
	select {
	case <-hp.eventsDone:
	case <-g.sess.Done():
		hp.closeEvents()
	case <-timer.C:
		g.log.Warn("the holder's events did not drain before the guest stopped")
		hp.closeEvents()
	}
}
