package global

import (
	"github.com/viderstv/common/instance"
	"github.com/viderstv/muxer/src/monitoring/prometheus"
)

type Instances struct {
	Redis      instance.Redis
	Mongo      instance.Mongo
	Prometheus prometheus.Instance
}
