package tools

// RuleRefusal is a tool refusal under a fixed rule (for example a lead-only
// tool called by an agent, or a plan outside its limits): repeating the same
// call cannot succeed until the rule is met. NextStep names what to do
// instead. It is recognised by type (errors.As), never by its text, so a
// real failure whose output merely quotes a refusal is not mistaken for one.
type RuleRefusal struct {
	Msg      string
	NextStep string
	// Advisory marks a refusal the caller routes around (it does not block
	// the outcome of the work it was part of).
	Advisory bool
}

func (r *RuleRefusal) Error() string { return r.Msg }

// Refuse returns a RuleRefusal with the message and next step.
func Refuse(msg, nextStep string) error { return &RuleRefusal{Msg: msg, NextStep: nextStep} }

// LeadOnlyNextStep is the NextStep of a lead-only agent tool called by an
// agent or child.
const LeadOnlyNextStep = "Only the lead can do this; report back to the lead instead of retrying."
