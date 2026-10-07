// Package trawl defines the types of trawl's JSON output and YAML config.
//
// Use it to decode the output of the trawl command or to build a config
// file. The analysis itself is run by the trawl command; this package has
// no analysis API. docs/OUTPUT-FORMAT.md describes which output changes
// a minor release may make.
package trawl

// ServiceType identifies the category of an external service matched by an indicator.
// User-defined service types can be expressed as ServiceType("CUSTOM").
// The set is open: consumers must accept values not listed here.
type ServiceType string

// Built-in service type constants.
const (
	ServiceTypeHTTP          ServiceType = "HTTP"
	ServiceTypeGRPC          ServiceType = "GRPC"
	ServiceTypeRedis         ServiceType = "REDIS"
	ServiceTypePubSub        ServiceType = "PUBSUB"
	ServiceTypeDatastore     ServiceType = "DATASTORE"
	ServiceTypeFirestore     ServiceType = "FIRESTORE"
	ServiceTypePostgres      ServiceType = "POSTGRES"
	ServiceTypeElasticsearch ServiceType = "ELASTICSEARCH"
	ServiceTypeVault         ServiceType = "VAULT"
	ServiceTypeEtcd          ServiceType = "ETCD"
)

// AnalysisStats holds diagnostic measurements from a single analysis run.
// All duration fields are wall-clock milliseconds measured during the run.
// Stats is only populated when the --stats flag is provided.
type AnalysisStats struct {
	PackagesLoaded     int   `json:"packages_loaded"`     // total packages loaded transitively
	PackagesAnalyzed   int   `json:"packages_analyzed"`   // packages whose function bodies were built
	DependencyPackages int   `json:"dependency_packages"` // non-initial packages auto-selected for bodies
	CallGraphNodes     int   `json:"call_graph_nodes"`    // total functions in the call graph
	CallGraphEdges     int   `json:"call_graph_edges"`    // total call sites in the call graph
	NodesVisited       int   `json:"nodes_visited"`       // functions entered during DFS
	EdgesExamined      int   `json:"edges_examined"`      // total edges considered during DFS (including skipped)
	UnresolvedInvokes  int   `json:"unresolved_invokes"`  // interface call sites with no concrete callee (stdlib/ubiquitous excluded)
	LoadDurationMs     int64 `json:"load_duration_ms"`    // milliseconds spent loading packages
	WalkDurationMs     int64 `json:"walk_duration_ms"`    // milliseconds spent walking the call graph
}

// Result holds the analysis output for a single entry point function.
type Result struct {
	EntryPoint    string         `json:"entry_point"`
	Package       string         `json:"package"`
	ExternalCalls []ExternalCall `json:"external_calls"`
	Deduplicated  bool           `json:"deduplicated,omitempty"`
	Stats         *AnalysisStats `json:"stats,omitempty"`
}

// ExternalCall describes a single detected call to an external service reachable from the entry point.
type ExternalCall struct {
	ServiceType    ServiceType `json:"service_type"`     // matched service label, e.g. ServiceTypeRedis
	ImportPath     string      `json:"import_path"`      // Go import path of the called package
	Function       string      `json:"function"`         // fully-qualified SSA function name
	File           string      `json:"file"`             // source file containing the call site
	Line           int         `json:"line"`             // line number of the call site
	CallChain      []string    `json:"call_chain"`       // ordered function names from entry point to call site; never nil in valid results
	ResolvedVia    ResolvedVia `json:"resolved_via"`     // how the call was discovered
	Confidence     Confidence  `json:"confidence"`       // reliability of the detection
	ShortFunction  string      `json:"short_function"`   // Function with module paths and generic type params stripped
	ShortCallChain []string    `json:"short_call_chain"` // CallChain with module paths and generic type params stripped
}

// ResolvedVia describes how an external call was discovered.
// The set is open: a minor release may add values, so consumers must accept
// unknown ones (Confidence tells how far to trust such a record).
type ResolvedVia string

// ResolvedVia values.
const (
	ResolvedViaDirect               ResolvedVia = "direct"
	ResolvedViaMockInference        ResolvedVia = "mock_inference"
	ResolvedViaCrossModuleInference ResolvedVia = "cross_module_inference"
	ResolvedViaCrossModuleTrace     ResolvedVia = "cross_module_trace" // dependency body walked to a backend; attributed to the module-side call
	ResolvedViaInterfaceDispatch    ResolvedVia = "interface_dispatch" // interface call with no concrete callee, classified by the interface's package
)

// Confidence indicates the reliability of a detection.
// The set is closed: it is always one of the constants below, and adding a
// level would be a major version change.
type Confidence string

// Confidence values, from most to least reliable.
const (
	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
	ConfidenceLow    Confidence = "low"
)

// Indicator maps an import path prefix to a named service type for detection purposes.
// When SkipInternal is true, subpackages under /internal/ within the indicator prefix
// are excluded from matching, preventing false positives from library internals.
// WrapperFor lists additional import path prefixes that should be classified under
// the same ServiceType. This allows declaring wrapper libraries explicitly so that
// calls through them receive a direct, high-confidence classification.
type Indicator struct {
	Package      string      `yaml:"package"      json:"package"`
	ServiceType  ServiceType `yaml:"service_type" json:"service_type"`
	SkipInternal bool        `yaml:"skip_internal,omitempty" json:"skip_internal,omitempty"`
	WrapperFor   []string    `yaml:"wrapper_for,omitempty"   json:"wrapper_for,omitempty"`
}

// Config holds user-supplied analysis configuration loaded from a YAML file.
type Config struct {
	Indicators []Indicator `yaml:"indicators"`
}
