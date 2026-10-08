package managementhttp

import (
	"crypto/sha256"
	"sync"

	cpasdkapi "github.com/router-for-me/CLIProxyAPI/v8/sdk/api"
	cpaconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	appconfig "github.com/router-for-me/CLIProxyAPIHome/internal/config"
	"gopkg.in/yaml.v3"
)

// handlerConfigSync applies configs to the shared SDK management handler only
// when their source changed. The SDK handler reads its config without a lock
// while authenticating, so it must not be rewritten on every request.
type handlerConfigSync struct {
	mu      sync.Mutex
	handler *cpasdkapi.Handler
	source  any
	cfg     *cpaconfig.Config
}

// newHandlerConfigSync creates a config sync for the given SDK handler.
func newHandlerConfigSync(handler *cpasdkapi.Handler) *handlerConfigSync {
	return &handlerConfigSync{handler: handler}
}

// applyRuntimeConfig applies the CPA view of the Home runtime config. Home
// replaces the runtime config pointer on every change, so the pointer
// identifies the config version.
func (s *handlerConfigSync) applyRuntimeConfig(homeCfg *appconfig.Config) *cpaconfig.Config {
	return s.apply(homeCfg, func() *cpaconfig.Config {
		return cpaConfigFromHomeConfig(homeCfg)
	})
}

// applyLoadedConfig applies a config freshly loaded from the config file,
// identified by a fingerprint of its content.
func (s *handlerConfigSync) applyLoadedConfig(cfg *cpaconfig.Config) *cpaconfig.Config {
	var source any = new(byte)
	if data, errMarshal := yaml.Marshal(cfg); errMarshal == nil {
		source = sha256.Sum256(data)
	}
	return s.apply(source, func() *cpaconfig.Config { return cfg })
}

// apply returns the config applied for source. It builds a config and hands it
// to the SDK handler only when source differs from the last applied source.
// source must be comparable.
func (s *handlerConfigSync) apply(source any, build func() *cpaconfig.Config) *cpaconfig.Config {
	if s == nil {
		return nonNilCPAConfig(build())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg != nil && s.source == source {
		return s.cfg
	}
	cfg := nonNilCPAConfig(build())
	if s.handler != nil {
		s.handler.SetConfig(cfg)
	}
	s.source = source
	s.cfg = cfg
	return cfg
}

// nonNilCPAConfig returns cfg, or an empty config when cfg is nil.
func nonNilCPAConfig(cfg *cpaconfig.Config) *cpaconfig.Config {
	if cfg == nil {
		return &cpaconfig.Config{}
	}
	return cfg
}
