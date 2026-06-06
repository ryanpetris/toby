package control

// Host-identity sentinels: a uid/gid of HostUser/HostGroup means "resolve to the
// host user/group." The sandbox runtime uses them when provisioning files and
// running commands so artifacts are owned by the invoking host user.
const (
	HostUser  = -2
	HostGroup = -2
)
