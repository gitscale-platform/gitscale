package invalidator

import (
	"errors"
	"os"
	"strings"
)

// EnvConfig is the runtime configuration sourced from process env. The cmd
// binary reads it once at boot; in-process tests construct a Config directly
// and skip this layer.
type EnvConfig struct {
	KafkaBootstrapServers []string
	KafkaTopic            string
	KafkaGroupID          string
	KafkaAutoOffsetReset  string // "earliest" | "latest"
	RedisURL              string
	RedisUseCluster       bool
	Env                   string // "dev" | "stg" | "prod" — used for cache key namespacing
}

// LoadEnv reads required env vars and returns the populated config. Caller
// is expected to fail fast on error.
func LoadEnv() (EnvConfig, error) {
	cfg := EnvConfig{
		KafkaTopic:           getOr("IDENTITY_INVALIDATOR_TOPIC", "gitscale.identity.events"),
		KafkaGroupID:         getOr("IDENTITY_INVALIDATOR_GROUP", "gitscale.identity-cache-invalidator"),
		KafkaAutoOffsetReset: getOr("KAFKA_AUTO_OFFSET_RESET", "earliest"),
		RedisURL:             os.Getenv("REDIS_URL"),
		RedisUseCluster:      os.Getenv("REDIS_USE_CLUSTER") == "true",
		Env:                  getOr("GITSCALE_ENV", "dev"),
	}

	bs := strings.TrimSpace(os.Getenv("KAFKA_BOOTSTRAP_SERVERS"))
	if bs == "" {
		return cfg, errors.New("invalidator: KAFKA_BOOTSTRAP_SERVERS is required")
	}
	for _, s := range strings.Split(bs, ",") {
		if s = strings.TrimSpace(s); s != "" {
			cfg.KafkaBootstrapServers = append(cfg.KafkaBootstrapServers, s)
		}
	}

	if cfg.RedisURL == "" {
		return cfg, errors.New("invalidator: REDIS_URL is required")
	}
	if cfg.KafkaAutoOffsetReset != "earliest" && cfg.KafkaAutoOffsetReset != "latest" {
		return cfg, errors.New("invalidator: KAFKA_AUTO_OFFSET_RESET must be 'earliest' or 'latest'")
	}
	return cfg, nil
}

func getOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
