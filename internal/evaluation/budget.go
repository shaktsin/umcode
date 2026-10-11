package evaluation

import (
	"errors"
	"fmt"
)

type Reservation struct {
	Maximum  float64  `json:"maximum"`
	Observed *float64 `json:"observed"`
	Settled  bool     `json:"settled"`
}
type Budget struct {
	Ceiling      float64                `json:"ceiling"`
	Reservations map[string]Reservation `json:"reservations"`
}

func (b *Budget) Spent() float64 {
	v := 0.
	for _, r := range b.Reservations {
		if r.Settled && r.Observed != nil {
			v += *r.Observed
		} else {
			v += r.Maximum
		}
	}
	return v
}
func (b *Budget) Reserve(id string, maximum float64) error {
	if id == "" || !finite(maximum) || !finite(b.Ceiling) {
		return errors.New("invalid reservation")
	}
	if b.Reservations == nil {
		b.Reservations = map[string]Reservation{}
	}
	if _, ok := b.Reservations[id]; ok {
		return errors.New("duplicate reservation")
	}
	if b.Ceiling > 0 && b.Spent()+maximum > b.Ceiling {
		return fmt.Errorf("budget exhausted: request needs %.8f USD", maximum)
	}
	b.Reservations[id] = Reservation{Maximum: maximum}
	return nil
}
func (b *Budget) Settle(id string, observed *float64) error {
	r, ok := b.Reservations[id]
	if !ok || r.Settled {
		return errors.New("unknown/already settled reservation")
	}
	if observed != nil && !finite(*observed) {
		return errors.New("invalid settlement")
	}
	r.Settled = true
	if observed != nil {
		v := *observed
		r.Observed = &v
	}
	b.Reservations[id] = r
	return nil
}
func MaximumCost(m Model) float64 {
	return (float64(m.Window)*m.Price.Input + float64(m.MaxOutput)*m.Price.Output) / 1e6
}
