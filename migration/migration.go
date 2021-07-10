package migration

const (
	DefaultRsyncImage          = "docker.io/utkuozdemir/pv-migrate-rsync:alpine-3-20210522"
	DefaultSshdImage           = "docker.io/utkuozdemir/pv-migrate-sshd:alpine-3-20210522"
	DefaultIgnoreMounted       = false
	DefaultNoChown             = false
	DefaultSourceMountReadOnly = true
)

type PVC struct {
	KubeconfigPath string
	Context        string
	Namespace      string
	Name           string
	Path           string
}

type Migration struct {
	Source     *PVC
	Dest       *PVC
	Options    *Options
	Strategies []string
	RsyncImage string
	SshdImage  string
}

type Options struct {
	DeleteExtraneousFiles bool
	IgnoreMounted         bool
	NoChown               bool
	SourceMountReadOnly   bool
	KeyAlgorithm          string
}
