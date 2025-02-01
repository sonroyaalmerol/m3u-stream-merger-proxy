package loadbalancer

type LBConfig struct {
	MaxRetries int
}

func NewDefaultLBConfig() *LBConfig {
	return &LBConfig{
		MaxRetries: 3,
	}
}
