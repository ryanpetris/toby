package control

// Host-identity sentinels: a uid/gid of HostUser/HostGroup in command and file
// params means "resolve to the host user/group on the host side." They are wire
// values shared by the file and command capabilities, so they live with the
// transport rather than in any single capability package.
const (
	HostUser  = -2
	HostGroup = -2
)
