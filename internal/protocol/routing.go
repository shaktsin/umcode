package protocol

import "time"

// ConfiguredModel is a model the user has approved for use. The provider
// catalog is a list of suggestions; this is the list the router may actually
// spend money on. Authentication comes from the provider's keys and is
// deliberately not a separate user-facing concept here.
type ConfiguredModel struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Enabled  bool   `json:"enabled"`
}

// ModelPool is a named set of configured models to route among. Strategy says
// how to order them: "priority" keeps the order the user gave, and the others
// rank by what the complexity tiers rank by.
type ModelPool struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Strategy string   `json:"strategy"`
	Models   []string `json:"models"`
	Enabled  bool     `json:"enabled"`
}

// Pool strategies.
const (
	PoolPriority = "priority" // the order the user listed
	PoolBalanced = "balanced" // the Standard tier ranking
	PoolQuality  = "quality"  // the Deep tier ranking
	PoolFast     = "fast"     // the Quick tier ranking
	PoolCheap    = "cheap"    // cheapest first
)

// RoutingConfig is the whole registry: what may be used, and how it is grouped.
type RoutingConfig struct {
	Models      []ConfiguredModel `json:"models"`
	Pools       []ModelPool       `json:"pools"`
	DefaultPool string            `json:"defaultPool,omitempty"`
}

// PoolProvider is the pseudo-provider a selection uses to name a pool rather
// than a single model: {provider: "pool", model: "<pool id>"}.
const PoolProvider = "pool"

// Routing methods and events.
const (
	// MethodModelRoute is a dry run: what would this chat use right now, and
	// what would it fall back to.
	MethodModelRoute  = "model/route"
	MethodModelHealth = "model/health"

	// NotifyRouteChanged is sent when a turn moves to a different model or key
	// mid-flight, so the UI can say so rather than stalling silently.
	NotifyRouteChanged = "turn/routeChanged"
)

// Why a route was chosen, for the UI and the turn's trail.
const (
	RoutePinned   = "pinned"    // the user chose this model
	RouteDefault  = "default"   // the chat's or project's default
	RouteTier     = "tier"      // the complexity level picked it
	RouteFallback = "fallback"  // an earlier route failed
	RouteLastGasp = "last tier" // nothing left in the tier, dropping down
)

// RouteInfo is one candidate: a model, the key that would pay for it, and why
// it is in the list.
type RouteInfo struct {
	Provider     string  `json:"provider"`
	Model        string  `json:"model"`
	DisplayName  string  `json:"displayName,omitempty"`
	CredentialID string  `json:"credentialId"`
	CredentialL  string  `json:"credentialLabel,omitempty"`
	Why          string  `json:"why"`
	CostPerMTok  float64 `json:"costPerMTok,omitempty"`
	// Unavailable is set when the route is known to be unusable right now; the
	// reason says why (cooling down after a rate limit, budget spent, …).
	Unavailable string     `json:"unavailable,omitempty"`
	CooldownEnd *time.Time `json:"cooldownEnd,omitempty"`
}

// RouteStep is one attempt recorded on a turn.
type RouteStep struct {
	RouteInfo
	At      time.Time `json:"at"`
	Status  string    `json:"status"` // used | rateLimited | failed | skipped
	Error   string    `json:"error,omitempty"`
	Latency int64     `json:"latencyMs,omitempty"`
}

// ModelRouteParams asks what a request would run on. Everything is optional:
// with no thread the engine answers for a new chat in the given project.
type ModelRouteParams struct {
	ThreadID   string         `json:"threadId,omitempty"`
	ProjectID  string         `json:"projectId,omitempty"`
	Override   ModelSelection `json:"override,omitempty"`
	Complexity Complexity     `json:"complexity,omitempty"`
	// Pool names a configured pool to route within, as "pool/<id>" does in a
	// selection.
	Pool string `json:"pool,omitempty"`
	Role string `json:"role,omitempty"`
}

type ModelRouteResult struct {
	// Chosen is what the next message would use; Alternatives are what it
	// would fall back to, in order.
	Chosen       *RouteInfo  `json:"chosen,omitempty"`
	Alternatives []RouteInfo `json:"alternatives"`
	Complexity   Complexity  `json:"complexity"`
	// AutoPicked is always false since Auto was removed; kept for wire compatibility.
	AutoPicked bool `json:"autoPicked,omitempty"`
	// Reason explains an empty list: no keys, everything cooling down, …
	Reason string `json:"reason,omitempty"`
}

// ModelHealthRow is what the router has learned about one key and model.
type ModelHealthRow struct {
	Provider     string     `json:"provider"`
	Model        string     `json:"model"`
	CredentialID string     `json:"credentialId"`
	Label        string     `json:"credentialLabel,omitempty"`
	CooldownEnd  *time.Time `json:"cooldownEnd,omitempty"`
	LastStatus   string     `json:"lastStatus,omitempty"`
	LastError    string     `json:"lastError,omitempty"`
	OKCount      int64      `json:"okCount"`
	ErrCount     int64      `json:"errCount"`
	LatencyMs    int64      `json:"latencyMs,omitempty"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

// ModelHealthParams reads the table, and optionally lets one route be tried
// again before its cooldown is up.
type ModelHealthParams struct {
	Clear *RouteRef `json:"clear,omitempty"`
}

// RouteRef names one key and model.
type RouteRef struct {
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	CredentialID string `json:"credentialId"`
}

type ModelHealthResult struct {
	Rows []ModelHealthRow `json:"rows"`
}

// RouteChangedEvent says a running turn moved to another route.
type RouteChangedEvent struct {
	ThreadID string    `json:"threadId"`
	TurnID   string    `json:"turnId"`
	From     RouteInfo `json:"from"`
	To       RouteInfo `json:"to"`
	Reason   string    `json:"reason"`
}
