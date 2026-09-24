package decode

// Standard resource bounds and safety limits across format decoders.
const (
	MaxPayloadBytes = 10 * 1024 * 1024 // 10MB maximum raw payload size
	MaxChildren     = 10000            // 10,000 maximum child records per batch
	MaxFields       = 1000             // 1,000 maximum fields per decoded record
	MaxDepth        = 32               // 32 maximum nesting depth for hierarchical objects
	MaxTextLine     = 64 * 1024        // 64KB maximum line length for text/regex
)
