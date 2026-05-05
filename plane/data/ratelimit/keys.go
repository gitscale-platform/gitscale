package ratelimit

// TokenBucketKey is the key template for a rate-limit bucket.
// %s[0] = principal UUID, %s[1] = surface enum (e.g. "git_push", "pr_open").
// Surface enum values must not contain ":" as that conflicts with the key separator.
//
// The env-namespace prefix ("gitscale:<env>:") is applied by WithNamespace and
// must NOT be embedded here.
const TokenBucketKey = "rl:bucket:%s:%s"
