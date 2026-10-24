package circuitbreaker

import (
	"sync"
)

// Permit represents an admitted execution permit that must receive exactly one terminal outcome.
type Permit interface {
	RecordSuccess()
	RecordFailure()
	ReleaseCancel()
}

type circuitPermit struct {
	cb    *CircuitBreaker
	state State
	once  sync.Once
}

func (p *circuitPermit) RecordSuccess() {
	p.once.Do(func() {
		p.cb.onPermitSuccess(p.state)
	})
}

func (p *circuitPermit) RecordFailure() {
	p.once.Do(func() {
		p.cb.onPermitFailure(p.state)
	})
}

func (p *circuitPermit) ReleaseCancel() {
	p.once.Do(func() {
		p.cb.onPermitCancel(p.state)
	})
}

type noopPermit struct{}

func (noopPermit) RecordSuccess() {}
func (noopPermit) RecordFailure() {}
func (noopPermit) ReleaseCancel() {}
