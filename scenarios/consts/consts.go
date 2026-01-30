package consts

import (
	"log/slog"
)

var (
	UserID         = "example.com"
	SlogLevel      = slog.LevelInfo
	KubeconfigPath = "config/kwok-cluster/kubeconfig.yaml"
	EtcdPrefix     = "/kube-scheduler-evaluator"
	EtcdURL        = "http://localhost:2379"
)
